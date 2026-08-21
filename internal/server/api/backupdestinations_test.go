package api

import (
	"encoding/json"
	"strings"
	"testing"

	"servermonitor/internal/server/storage"
)

func TestBackupDestinationAdminViewNeverContainsSecrets(t *testing.T) {
	view := toBackupDestinationView(storage.BackupDestination{
		ID:                    1,
		Name:                  "R2",
		Kind:                  storage.BackupDestinationDirectS3,
		Bucket:                "backup-bucket",
		CredentialsConfigured: true,
	})
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(data)
	for _, forbidden := range []string{"access_key_id", "secret_access_key", "session_token"} {
		if strings.Contains(jsonText, forbidden) {
			t.Fatalf("admin response contains secret field %q: %s", forbidden, jsonText)
		}
	}
	if !strings.Contains(jsonText, `"credentials_configured":true`) {
		t.Fatalf("admin response lost safe credential status: %s", jsonText)
	}
}
