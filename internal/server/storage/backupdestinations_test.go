package storage

import "testing"

func TestExpandDirectPrefixScopesByHostAndRepository(t *testing.T) {
	got, err := ExpandDirectPrefix(DefaultDirectPrefixTemplate, 17, "web / prod", "nightly backup")
	if err != nil {
		t.Fatalf("ExpandDirectPrefix: %v", err)
	}
	if want := "backups/hosts/17-web-prod/nightly-backup"; got != want {
		t.Fatalf("prefix = %q, want %q", got, want)
	}
}

func TestDestinationRequiresScopedTemplate(t *testing.T) {
	input := normalizeDestinationInput(BackupDestinationInput{
		Name:           "R2",
		Bucket:         "backups",
		PrefixTemplate: "all-hosts/{repository}",
	})
	if err := validateDestinationInput(input); err == nil {
		t.Fatal("destination accepted a prefix without {host_id}")
	}
	input.PrefixTemplate = "all-hosts/{host_id}"
	if err := validateDestinationInput(input); err == nil {
		t.Fatal("destination accepted a prefix without {repository}")
	}
	input.PrefixTemplate = "all/{host_id}/{repository}/{unknown}"
	if err := validateDestinationInput(input); err == nil {
		t.Fatal("destination accepted an unknown placeholder")
	}
}

func TestDestinationRequiresSecureEndpointAndSafeLocation(t *testing.T) {
	base := BackupDestinationInput{
		Name:           "R2",
		Bucket:         "backups",
		PrefixTemplate: DefaultDirectPrefixTemplate,
	}
	for _, endpoint := range []string{"http://minio.example.test", "https://user:pass@s3.example.test", "https://s3.example.test/path"} {
		input := normalizeDestinationInput(base)
		input.Endpoint = endpoint
		if err := validateDestinationInput(input); err == nil {
			t.Fatalf("accepted unsafe endpoint %q", endpoint)
		}
	}
	input := normalizeDestinationInput(base)
	input.Endpoint = "https://account.r2.cloudflarestorage.com"
	input.Region = "auto"
	if err := validateDestinationInput(input); err != nil {
		t.Fatalf("rejected secure S3 endpoint: %v", err)
	}
	input.Bucket = "bad/bucket"
	if err := validateDestinationInput(input); err == nil {
		t.Fatal("accepted bucket containing a path")
	}
}

func TestDirectS3RepositoryURL(t *testing.T) {
	cases := []struct{ endpoint, bucket, prefix, want string }{
		{"", "bucket", "hosts/1/repo", "s3:s3.amazonaws.com/bucket/hosts/1/repo"},
		{"https://account.r2.cloudflarestorage.com/", "/bucket/", "/hosts/1/repo/", "s3:https://account.r2.cloudflarestorage.com/bucket/hosts/1/repo"},
	}
	for _, tc := range cases {
		if got := DirectS3RepositoryURL(tc.endpoint, tc.bucket, tc.prefix); got != tc.want {
			t.Errorf("DirectS3RepositoryURL(%q) = %q, want %q", tc.endpoint, got, tc.want)
		}
	}
}

func TestBackupS3CredentialsValidation(t *testing.T) {
	if err := (BackupS3Credentials{AccessKeyID: "key"}).Validate(); err == nil {
		t.Fatal("accepted access key without secret")
	}
	if err := (BackupS3Credentials{AccessKeyID: "key", SecretAccessKey: "secret\nleak"}).Validate(); err == nil {
		t.Fatal("accepted newline in secret")
	}
	if err := (BackupS3Credentials{AccessKeyID: "key", SecretAccessKey: "secret"}).Validate(); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}
}
