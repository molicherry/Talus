package usage

import "testing"

func TestConnectionMetadataIsBoundedAndCannotCarrySecrets(t *testing.T) {
	valid := map[string]any{"connection_mode": "ssh_jump", "connection_role": "jump", "connection_stage": "jump_auth", "connection_revision": uint64(2), "jump_server_id": uint(3)}
	for key, value := range valid {
		if _, ok := normalizeMetadata(key, value); !ok {
			t.Errorf("dropped valid %s", key)
		}
	}
	invalid := map[string]any{"proxy_password": "secret", "connection_stage": "password=secret", "connection_mode": "socks5://user:secret@proxy", "connection_role": "user:secret", "jump_server_id": uint(0), "connection_revision": -1}
	for key, value := range invalid {
		if _, ok := normalizeMetadata(key, value); ok {
			t.Errorf("accepted unsafe %s", key)
		}
	}
	for _, reason := range []string{"invalid_connection_config", "connection_config_changed", "connection_in_use", "proxy_connection_failed", "proxy_auth_failed", "proxy_connect_rejected", "jump_forward_failed"} {
		if got := safeReason(reason); got != reason {
			t.Errorf("reason %s became %s", reason, got)
		}
	}
}
