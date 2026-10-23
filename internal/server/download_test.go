package server

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestInstallCommand(t *testing.T) {
	s := &Server{cfg: &Config{BaseURL: "https://cal.example.com"}}
	command := s.installCommand(&User{Username: "alice", Token: "secret"})
	_, encoded, ok := strings.Cut(command, "-EncodedCommand ")
	if !ok {
		t.Fatal(command)
	}
	b, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		t.Fatal(e)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	decoded := string(utf16.Decode(u))
	for _, want := range []string{"https://cal.example.com/client/install.ps1", "-Username 'alice'", "-Token 'secret'", "finally"} {
		if !strings.Contains(decoded, want) {
			t.Fatal("missing", want)
		}
	}
}
