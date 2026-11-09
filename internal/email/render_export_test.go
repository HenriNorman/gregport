package email

import (
	"strings"
	"testing"
)

func TestRenderExportReady(t *testing.T) {
	const url = "https://storage.test/get/export.csv?signature=abc"
	rendered, err := renderEmail(TemplateExportReady, DeliveryPayload{
		OrgName:    "Acme",
		Subject:    "Your ticket export is ready",
		ExportURL:  url,
		ExportRows: 42,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if rendered.Subject != "Your ticket export is ready" {
		t.Fatalf("subject=%q", rendered.Subject)
	}
	for _, body := range []string{rendered.Text, rendered.HTML} {
		if !strings.Contains(body, url) || !strings.Contains(body, "42") {
			t.Fatalf("body missing link or row count: %q", body)
		}
	}
}
