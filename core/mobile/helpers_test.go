package mobile

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/ShravanAmudala55/ephdrop/core/discovery"
	"github.com/ShravanAmudala55/ephdrop/core/identity"
)

type sighting = discovery.Sighting
type idType = identity.DeviceID

func openFile(p string) (*os.File, error) { return os.Open(p) }
func itoa(n int) string                   { return strconv.Itoa(n) }

func sightingFor(t *testing.T, id string) discovery.Sighting {
	return discovery.Sighting{ID: identity.DeviceID(id), Addrs: []string{"127.0.0.1:4000"}}
}

func decodeInvite(raw string) ([]string, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var j struct {
		A []string `json:"a"`
	}
	return j.A, json.Unmarshal(b, &j)
}
