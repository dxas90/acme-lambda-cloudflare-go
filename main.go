package main

// CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-extldflags "-static"' -o bootstrap .
import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/secretsmanager"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
)

type AcmeUser struct {
	Email        string
	Registration *registration.Resource
	Key          crypto.PrivateKey
}

var (
	cfapiToken       string
	letsencryptEmail string
)

func (u *AcmeUser) GetEmail() string                        { return u.Email }
func (u *AcmeUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *AcmeUser) GetPrivateKey() crypto.PrivateKey        { return u.Key }

const (
	privateKeyFile   = "acme_user_privkey.pem"
	registrationFile = "acme_user_registration.json"
)

func createUser(email string) (*AcmeUser, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return &AcmeUser{Email: email, Key: privateKey}, nil
}

func createClient(user *AcmeUser) (*lego.Client, error) {
	config := lego.NewConfig(user)
	config.Certificate.KeyType = certcrypto.RSA2048
	useProduction := os.Getenv("USE_PRODUCTION_CA") == "true"
	if useProduction {
		config.CADirURL = lego.LEDirectoryProduction
	} else {
		config.CADirURL = lego.LEDirectoryStaging
	}

	client, err := lego.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("create lego client: %w", err)
	}

	os.Setenv("CLOUDFLARE_DNS_API_TOKEN", cfapiToken)

	provider, err := cloudflare.NewDNSProvider()
	if err != nil {
		return nil, fmt.Errorf("create DNS provider: %w", err)
	}

	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return nil, fmt.Errorf("set DNS provider: %w", err)
	}

	return client, nil
}

func obtainAndUploadCertificates(client *lego.Client, domains []string, s3Bucket, region string) error {
	request := certificate.ObtainRequest{Domains: domains, Bundle: true}

	certs, err := client.Certificate.Obtain(request)
	if err != nil {
		return fmt.Errorf("obtain certificate: %w", err)
	}

	files := map[string][]byte{
		"cert.pem":      certs.Certificate,
		"fullchain.pem": certs.Certificate,
		"privkey.pem":   certs.PrivateKey,
	}

	for filename, data := range files {
		certPath := "/tmp/" + filename
		if err := os.WriteFile(certPath, data, 0600); err != nil {
			return fmt.Errorf("write file %s: %w", certPath, err)
		}
		uploadFileToS3(s3Bucket, region, certPath)
	}
	return nil
}

func loadUserFromS3(bucket, region string) (*AcmeUser, error) {
	sess := session.Must(session.NewSession(&aws.Config{Region: aws.String(region)}))
	s3client := s3.New(sess)

	privKeyObj, err := s3client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(privateKeyFile),
	})
	if err != nil {
		return nil, err
	}
	defer privKeyObj.Body.Close()

	keyData, err := io.ReadAll(privKeyObj.Body)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}

	block, _ := pem.Decode(keyData)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, fmt.Errorf("invalid PEM private key")
	}

	privKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	regObj, err := s3client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(registrationFile),
	})
	if err != nil {
		return nil, err
	}
	defer regObj.Body.Close()

	regData, err := io.ReadAll(regObj.Body)
	if err != nil {
		return nil, fmt.Errorf("read registration: %w", err)
	}

	var reg registration.Resource
	if err := json.Unmarshal(regData, &reg); err != nil {
		return nil, fmt.Errorf("unmarshal registration: %w", err)
	}
	var email string
	if len(reg.Body.Contact) > 0 {
		email = reg.Body.Contact[0]
	} else {
		email = letsencryptEmail // or fallback to some default or error
	}
	return &AcmeUser{Email: email, Key: privKey, Registration: &reg}, nil
}

func saveUserToS3(user *AcmeUser, bucket, region string) error {
	sess := session.Must(session.NewSession(&aws.Config{Region: aws.String(region)}))
	s3client := s3.New(sess)

	privKeyBytes, err := x509.MarshalECPrivateKey(user.Key.(*ecdsa.PrivateKey))
	if err != nil {
		return err
	}
	privKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privKeyBytes})

	_, err = s3client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(privateKeyFile), Body: bytes.NewReader(privKeyPEM),
	})
	if err != nil {
		return err
	}

	regData, err := json.Marshal(user.Registration)
	if err != nil {
		return err
	}

	_, err = s3client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(registrationFile), Body: bytes.NewReader(regData),
	})
	return err
}

func uploadFileToS3(bucket, region, s3filepath string) {
	sess := session.Must(session.NewSession(&aws.Config{Region: aws.String(region)}))
	s3client := s3.New(sess)

	file, err := os.Open(s3filepath)
	if err != nil {
		log.Fatalf("open file %s: %v", s3filepath, err)
	}
	defer file.Close()

	key := filepath.Base(s3filepath)
	_, err = s3client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: file,
	})
	if err != nil {
		log.Fatalf("upload %s to S3: %v", key, err)
	}
	log.Printf("Uploaded %s to S3 bucket %s", key, bucket)
}

func getSecretValue(client *secretsmanager.SecretsManager, secretName string) string {
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretName),
	}

	result, err := client.GetSecretValue(input)
	if err != nil {
		log.Fatalf("SecretsManager get secret %s: %v", secretName, err)
	}

	if result.SecretString != nil {
		return *result.SecretString
	}
	// If secret is binary (unlikely for tokens), you can decode here
	log.Fatalf("SecretsManager secret %s has no string value", secretName)
	return ""
}

func getSSMParameter(client *ssm.SSM, name string) string {
	param, err := client.GetParameter(&ssm.GetParameterInput{
		Name: aws.String(name), WithDecryption: aws.Bool(true),
	})
	if err != nil {
		log.Fatalf("SSM get parameter %s: %v", name, err)
	}
	return aws.StringValue(param.Parameter.Value)
}

func handleRequest(ctx context.Context) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	s3Bucket := os.Getenv("S3_BUCKET")
	if s3Bucket == "" {
		log.Fatal("Missing S3_BUCKET environment variable")
	}

	sess := session.Must(session.NewSession(&aws.Config{Region: aws.String(region)}))
	ssmClient := ssm.New(sess)
	smClient := secretsmanager.New(sess)

	getEnvParam := func(envVar string) string {
		path := os.Getenv(envVar)
		if path == "" {
			log.Fatalf("Missing required env var: %s", envVar)
		}
		return getSSMParameter(ssmClient, path)
	}
	getSecretEnvParam := func(envVar string) string {
		path := os.Getenv(envVar)
		if path == "" {
			log.Fatalf("Missing required env var: %s", envVar)
		}
		return getSecretValue(smClient, path)
	}

	cfapiToken = getSecretEnvParam("SM_CLOUDFLARE_API_TOKEN")
	cloudflareEmail := getEnvParam("SSM_CLOUDFLARE_EMAIL")
	zoneID := getEnvParam("SSM_CLOUDFLARE_ZONE_ID")
	letsencryptEmail = getEnvParam("SSM_LETSENCRYPT_EMAIL")
	domainCSV := getEnvParam("SSM_LETSENCRYPT_DOMAINS")
	domains := strings.Split(domainCSV, ",")

	os.Setenv("CLOUDFLARE_DNS_API_TOKEN", cfapiToken)
	os.Setenv("CLOUDFLARE_ZONE_ID", zoneID)
	os.Setenv("CLOUDFLARE_EMAIL", cloudflareEmail)

	user, err := loadUserFromS3(s3Bucket, region)
	if err != nil {
		log.Println("User not found in S3, creating new one...")
		user, err = createUser(letsencryptEmail)
		if err != nil {
			log.Fatalf("create user: %v", err)
		}
		client, err := createClient(user)
		if err != nil {
			log.Fatalf("lego client: %v", err)
		}
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			log.Fatalf("register user: %v", err)
		}
		user.Registration = reg
		if err := saveUserToS3(user, s3Bucket, region); err != nil {
			log.Fatalf("save user: %v", err)
		}
		if err := obtainAndUploadCertificates(client, domains, s3Bucket, region); err != nil {
			log.Fatalf("certificates: %v", err)
		}
	} else {
		log.Println("User loaded from S3")
		client, err := createClient(user)
		if err != nil {
			log.Fatalf("lego client: %v", err)
		}
		if err := obtainAndUploadCertificates(client, domains, s3Bucket, region); err != nil {
			log.Fatalf("certificates: %v", err)
		}
	}
}

func main() {
	lambda.Start(handleRequest)
}
