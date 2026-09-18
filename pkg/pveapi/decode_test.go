package pveapi

import "testing"

func TestDecodeAgentB64(t *testing.T) {
	t.Parallel()
	// Empty string returns empty.
	out, err := decodeAgentB64("")
	if err != nil || out != "" {
		t.Fatalf("empty: %q %v", out, err)
	}
	// Printable ASCII (including valid base64 chars) is returned verbatim;
	// the function treats printable input as already-decoded plain text.
	out, err = decodeAgentB64("aGVsbG8=")
	if err != nil || out != "aGVsbG8=" {
		t.Fatalf("printable: %q %v", out, err)
	}
	// A hex hash string (printable ASCII) is returned verbatim.
	hash := "8462561e8fbcf1d718878981d329a86d29324a14ffda74090715777231ccb295\n"
	out, err = decodeAgentB64(hash)
	if err != nil || out != hash {
		t.Fatalf("hash: %q %v", out, err)
	}
}
