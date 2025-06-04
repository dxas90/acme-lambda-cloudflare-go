package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
)

type LetsUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *LetsUser) GetEmail() string {
	return u.Email
}
func (u LetsUser) GetRegistration() *registration.Resource {
	return u.Registration
}
func (u *LetsUser) GetPrivateKey() crypto.PrivateKey {
	return u.key
}

func getSSMParameter(ssmClient *ssm.SSM, name string) string {
	param, err := ssmClient.GetParameter(&ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		log.Fatalf("Failed to get SSM parameter %s: %v", name, err)
	}
	return *param.Parameter.Value
}

func uploadToS3(bucket, region, filename string) {
	sess := session.Must(session.NewSession(&aws.Config{Region: aws.String(region)}))
	s3client := s3.New(sess)

	file, err := os.Open(filename)
	if err != nil {
		log.Fatalf("Failed to open file %s: %v", filename, err)
	}
	defer file.Close()

	key := filepath.Base(filename)

	_, err = s3client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   file,
	})
	if err != nil {
		log.Fatalf("Failed to upload file %s to S3: %v", filename, err)
	}

	fmt.Printf("Uploaded %s to bucket %s\n", key, bucket)
}

func handler(ctx context.Context) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}

	sess := session.Must(session.NewSession())
	ssmClient := ssm.New(sess)

	// Get SSM paths from env
	cfTokenPath := os.Getenv("CLOUDFLARE_API_TOKEN")
	if cfTokenPath == "" {
		log.Fatal("Missing CLOUDFLARE_API_TOKEN env var")
	}
	cfAPIToken := getSSMParameter(ssmClient, cfTokenPath)
	os.Setenv("CLOUDFLARE_API_TOKEN", cfAPIToken)

	cfZonePath := os.Getenv("CLOUDFLARE_ZONE_ID")
	cfZoneID := getSSMParameter(ssmClient, cfZonePath)
	os.Setenv("CLOUDFLARE_ZONE_ID", cfZoneID)

	emailPath := os.Getenv("LETSENCRYPT_EMAIL")
	email := getSSMParameter(ssmClient, emailPath)

	domainsPath := os.Getenv("LETSENCRYPT_DOMAINS")
	domainsCSV := getSSMParameter(ssmClient, domainsPath)
	domains := strings.Split(domainsCSV, ",")

	// Set required env vars for lego provider
	os.Setenv("CLOUDFLARE_API_TOKEN", cfAPIToken)
	os.Setenv("CLOUDFLARE_ZONE_ID", cfZoneID)

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}

	LetsUser := LetsUser{Email: email, key: privateKey}

	config := lego.NewConfig(&LetsUser)
	config.Certificate.KeyType = certcrypto.RSA2048
	config.CADirURL = lego.LEDirectoryProduction

	client, err := lego.NewClient(config)
	if err != nil {
		log.Fatal(err)
	}

	provider, err := cloudflare.NewDNSProvider()
	if err != nil {
		log.Fatal(err)
	}

	err = client.Challenge.SetDNS01Provider(provider)
	if err != nil {
		log.Fatal(err)
	}

	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		log.Fatal(err)
	}
	LetsUser.Registration = reg

	request := certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
	}
	certs, err := client.Certificate.Obtain(request)
	if err != nil {
		log.Fatal(err)
	}

	files := map[string][]byte{
		"cert.pem":      certs.Certificate,
		"fullchain.pem": certs.Certificate,
		"privkey.pem":   certs.PrivateKey,
	}

	s3Bucket := os.Getenv("S3_BUCKET")
	if s3Bucket == "" {
		log.Fatal("S3_BUCKET environment variable is not set")
	}

	for name, data := range files {
		err := os.WriteFile(name, data, 0644)
		if err != nil {
			log.Fatalf("Failed to write %s: %v", name, err)
		}
		uploadToS3(s3Bucket, region, name)
	}
}

func main() {
	lambda.Start(handler)
}
