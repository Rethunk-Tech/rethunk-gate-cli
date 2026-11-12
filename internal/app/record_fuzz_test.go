package app

import (
	"bytes"
	"encoding/json"
	"testing"
)

func FuzzRecordJSON(f *testing.F) {
	f.Add([]byte(`{
  "schema": 3,
  "root": "/tmp/proj",
  "head": "abc123",
  "dirty": false,
  "status": "deadbeef",
  "partial": false,
  "started": "2026-01-01T00:00:00Z",
  "finished": "2026-01-01T00:00:01Z",
  "exit": 0,
  "gates": []
}`))
	f.Add([]byte(`{
  "schema": 3,
  "root": "/src/app",
  "head": "def456",
  "dirty": true,
  "status": "cafebabe",
  "partial": true,
  "started": "2026-06-15T12:00:00.123456789Z",
  "finished": "2026-06-15T12:00:02Z",
  "exit": 1,
  "gates": [{"name": "test", "display": "go test", "status": "fail"}]
}`))
	f.Add([]byte(`{"schema":1,"root":"","head":"","dirty":false,"status":"","partial":false,"exit":0}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"schema":"nope"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var rec record
		if err := json.Unmarshal(data, &rec); err != nil {
			return
		}
		var buf bytes.Buffer
		if err := writeRecordText(&buf, rec); err != nil {
			t.Fatalf("writeRecordText: %v", err)
		}
		out, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal after unmarshal: %v", err)
		}
		var rec2 record
		if err := json.Unmarshal(out, &rec2); err != nil {
			t.Fatalf("re-unmarshal: %v", err)
		}
		out2, err := json.Marshal(rec2)
		if err != nil {
			t.Fatalf("second marshal: %v", err)
		}
		if !bytes.Equal(out, out2) {
			t.Fatalf("round-trip JSON changed:\n%s\n%s", out, out2)
		}
	})
}
