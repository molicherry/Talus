package service

import (
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
)

func TestServerFingerprintChangesWithConnectionIdentity(t *testing.T) {
	credID := uint(7)
	updated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	base := &model.Server{
		Host:         "host-a",
		Port:         22,
		CredentialID: &credID,
		Credential:   &model.SSHCredential{BaseModel: model.BaseModel{ID: credID, UpdatedAt: updated}},
	}

	same := &model.Server{
		Host:         "host-a",
		Port:         22,
		CredentialID: &credID,
		Credential:   &model.SSHCredential{BaseModel: model.BaseModel{ID: credID, UpdatedAt: updated}},
	}
	if serverFingerprint(base) != serverFingerprint(same) {
		t.Fatal("identical connection parameters produced different fingerprints")
	}

	otherHost := *base
	otherHost.Host = "host-b"
	if serverFingerprint(base) == serverFingerprint(&otherHost) {
		t.Fatal("host change did not change the fingerprint")
	}

	otherPort := *base
	otherPort.Port = 2222
	if serverFingerprint(base) == serverFingerprint(&otherPort) {
		t.Fatal("port change did not change the fingerprint")
	}

	otherCredID := uint(8)
	otherCred := *base
	otherCred.CredentialID = &otherCredID
	otherCred.Credential = &model.SSHCredential{BaseModel: model.BaseModel{ID: otherCredID, UpdatedAt: updated}}
	if serverFingerprint(base) == serverFingerprint(&otherCred) {
		t.Fatal("credential change did not change the fingerprint")
	}

	// Same credential id, rotated secret: the revision must be part of the key,
	// or old connections would keep being reused after a password change.
	rotated := *base
	rotated.Credential = &model.SSHCredential{
		BaseModel: model.BaseModel{ID: credID, UpdatedAt: updated.Add(time.Hour)},
	}
	if serverFingerprint(base) == serverFingerprint(&rotated) {
		t.Fatal("credential update did not change the fingerprint")
	}

	noCred := &model.Server{Host: "host-a", Port: 22}
	if serverFingerprint(noCred) == serverFingerprint(base) {
		t.Fatal("removing the credential did not change the fingerprint")
	}
}
