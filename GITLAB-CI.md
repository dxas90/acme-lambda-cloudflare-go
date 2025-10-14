# GitLab CI/CD Pipeline Documentation

This repository includes a comprehensive GitLab CI/CD pipeline that automates building, testing, containerizing, and deploying the ACME Lambda Cloudflare Go application.

## Pipeline Stages

The pipeline consists of 7 stages:

1. **initialize** - Downloads and verifies Go dependencies
2. **build** - Compiles the Go Lambda function into a bootstrap binary
3. **test** - Runs tests, linting, and code formatting checks
4. **containerize** - Builds and pushes Docker images (tags only)
5. **deployment** - Deploys to staging environment (tags only)
6. **promote** - Manual promotion to production (tags only)
7. **notify** - Sends notifications on success or failure

## Pipeline Behavior

### On Every Commit to Any Branch

The following jobs run automatically:
- `initialize` - Sets up the build environment
- `build` - Compiles the Go application
- `test` - Runs quality checks
- `notify:success` or `notify:failure` - Sends build status notifications

### On Tagged Commits Only

When you create a Git tag, additional jobs become available:
- `containerize` - Builds a Docker image and pushes it to the registry as `staging-{TAG}`
- `deploy:staging` - Automatically deploys the containerized image to the staging environment
- `promote:production` - **Manual job** to promote the staging image to production

## Usage

### Normal Development Workflow

1. Make changes to the code
2. Commit and push to any branch
3. Pipeline runs: initialize → build → test → notify

Example:
```bash
git add .
git commit -m "Add new feature"
git push origin feature-branch
```

### Release Workflow (Tags)

1. Create a Git tag for the release
2. Push the tag to trigger the full pipeline
3. Pipeline runs: initialize → build → test → containerize → deploy:staging → notify
4. Manually trigger `promote:production` when ready

Example:
```bash
git tag v1.0.0
git push origin v1.0.0
```

Then in GitLab:
- Go to CI/CD → Pipelines
- Find your tag pipeline
- Click the play button (▶) on `promote:production` to deploy to production

## Image Tagging Strategy

- **Staging images**: `{REGISTRY}/staging-{TAG}` (e.g., `registry.gitlab.com/user/repo:staging-v1.0.0`)
- **Production images**: `{REGISTRY}/production-{TAG}` (e.g., `registry.gitlab.com/user/repo:production-v1.0.0`)
- **Latest production**: `{REGISTRY}/latest` - Updated on every production promotion

## Environment Variables

The pipeline uses the following GitLab CI/CD variables:

### Built-in Variables (automatically available)
- `CI_REGISTRY` - GitLab Container Registry URL
- `CI_REGISTRY_IMAGE` - Full registry path for this project
- `CI_REGISTRY_USER` - Registry username
- `CI_REGISTRY_PASSWORD` - Registry password
- `CI_COMMIT_TAG` - Git tag name (only on tag pipelines)
- `CI_COMMIT_SHA` - Commit hash
- `CI_COMMIT_REF_NAME` - Branch or tag name

### Custom Variables (configure in GitLab)

You may want to add these in GitLab Settings → CI/CD → Variables:

- `SLACK_WEBHOOK_URL` - For Slack notifications (optional)
- `AWS_ACCESS_KEY_ID` - For AWS deployments (if using real AWS Lambda)
- `AWS_SECRET_ACCESS_KEY` - For AWS deployments (if using real AWS Lambda)
- `AWS_REGION` - AWS region for deployments

## Job Details

### Initialize Job
- **Image**: golang:1.24
- **Purpose**: Download and verify Go module dependencies
- **Artifacts**: Cached go.sum file
- **When**: On every push

### Build Job
- **Image**: golang:1.24
- **Purpose**: Compile the Lambda function for Linux
- **Artifacts**: bootstrap binary (1 day retention)
- **When**: On every push

### Test Job
- **Image**: golang:1.24
- **Purpose**: Run tests, linting (go vet), and format checks (gofmt)
- **When**: On every push

### Containerize Job
- **Image**: docker:latest
- **Purpose**: Build Docker image for AWS Lambda
- **Artifacts**: build.env with image information
- **When**: Only on Git tags
- **Requires**: Successful build job

### Deploy:Staging Job
- **Image**: alpine:latest
- **Purpose**: Deploy containerized image to staging
- **Environment**: staging
- **When**: Only on Git tags, automatically after containerize
- **Requires**: Successful containerize job

### Promote:Production Job
- **Image**: docker:latest
- **Purpose**: Tag and push staging image as production
- **Environment**: production
- **When**: Only on Git tags, **manual trigger required**
- **Requires**: Successful deploy:staging job

### Notify Jobs
- **Image**: alpine:latest
- **Purpose**: Send success/failure notifications
- **When**: After pipeline completes (success or failure)

## Customization

### Adding Real AWS Deployment

To deploy to actual AWS Lambda, modify the `deploy:staging` job:

```yaml
deploy:staging:
  stage: deployment
  image: amazon/aws-cli:latest
  script:
    - aws lambda update-function-code \
        --function-name acme-lambda-staging \
        --image-uri ${STAGING_IMAGE_TAG}
  environment:
    name: staging
```

### Adding Slack Notifications

Modify the notify jobs to include:

```yaml
script:
  - |
    curl -X POST -H 'Content-type: application/json' \
      --data "{\"text\":\"Build ${CI_PIPELINE_STATUS} for ${CI_COMMIT_REF_NAME}\"}" \
      $SLACK_WEBHOOK_URL
```

## Troubleshooting

### Pipeline doesn't run on tags
- Ensure you're pushing tags: `git push --tags` or `git push origin v1.0.0`
- Check that the tag was created successfully: `git tag -l`

### Docker build fails
- Verify `CI_REGISTRY_USER` and `CI_REGISTRY_PASSWORD` are set
- Check GitLab Container Registry is enabled for the project

### Tests fail
- Run tests locally first: `go test ./...`
- Check code formatting: `gofmt -l .`
- Run linter: `go vet ./...`

## Best Practices

1. **Always test locally** before pushing
2. **Create tags from stable commits** only
3. **Use semantic versioning** for tags (e.g., v1.0.0, v1.0.1)
4. **Monitor staging** before promoting to production
5. **Keep the pipeline fast** - typical run should be under 5 minutes
6. **Review logs** of each job to understand failures

## Pipeline Flow Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                    On Every Push                             │
├─────────────────────────────────────────────────────────────┤
│  initialize → build → test → notify                          │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│                     On Git Tags                              │
├─────────────────────────────────────────────────────────────┤
│  initialize → build → test → containerize → deploy:staging  │
│                                     ↓                        │
│                              promote:production (manual)     │
│                                     ↓                        │
│                                   notify                     │
└─────────────────────────────────────────────────────────────┘
```

## Support

For issues with the pipeline, check:
- GitLab CI/CD documentation: https://docs.gitlab.com/ee/ci/
- Docker documentation: https://docs.docker.com/
- AWS Lambda documentation: https://docs.aws.amazon.com/lambda/
