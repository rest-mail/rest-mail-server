package models

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDKIMRotationKey_EncryptionAndRedaction(t *testing.T) {
	const plain = "private-rotation-key-sentinel"
	key := DKIMRotationKey{Slot: DKIMRotationStaged, Selector: "restmail-2"}
	if err := key.SetPrivateKey(plain, testMasterKey); err != nil {
		t.Fatal(err)
	}
	if !DKIMKeyIsEncrypted(key.PrivateKey) || strings.Contains(key.PrivateKey, plain) {
		t.Fatal("rotation key was not encrypted")
	}
	if got, err := LoadDKIMPrivateKey(key.PrivateKey, testMasterKey); err != nil || got != plain {
		t.Fatal("rotation key did not round trip")
	}
	for _, master := range []string{"", "wrong-master"} {
		if got, err := LoadDKIMPrivateKey(key.PrivateKey, master); got != "" || !errors.Is(err, ErrDKIMKeyUndecryptable) {
			t.Fatal("rotation key did not fail closed")
		}
	}
	stored := key.PrivateKey
	if err := key.SetPrivateKey("", testMasterKey); err == nil || key.PrivateKey != stored {
		t.Fatal("empty key replaced the stored key")
	}
	key.Domain.DKIMPrivateKey = "current-private-sentinel"
	encoded, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{plain, stored, "private_key", "current-private-sentinel"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("serialized rotation key exposed private material")
		}
	}
}
