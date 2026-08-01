package credentials

import (
	"testing"

	"github.com/google/uuid"
)

func FuzzPayloadDecoder(f *testing.F) {
	f.Add([]byte(`{"v":1,"user":"u","password":"p"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"v":1}`))
	f.Add([]byte(`{"v":1,"user":"","password":""}`))
	f.Add([]byte(`invalid json`))
	f.Add([]byte(`{"v":1,"user":"u","password":"p","extra":1}`))
	f.Add(make([]byte, 5000))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodePayload(data)
	})
}

func FuzzAAD(f *testing.F) {
	f.Add(make([]byte, 48))
	f.Add(make([]byte, 0))
	f.Add(make([]byte, 100))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) >= 32 {
			var ws, ref uuid.UUID
			copy(ws[:], data[0:16])
			copy(ref[:], data[16:32])
			_ = BuildAAD(DataAADTag, ws, ref, 1, SuiteAES256GCMv1, 1)
		}
	})
}
