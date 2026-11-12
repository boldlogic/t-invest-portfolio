package reportapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"t-invest-portfolio/internal/brokerreport"
	"t-invest-portfolio/internal/store"
)

type fakeImporter struct {
	calls  int
	result store.ReportImportResult
}

func (f *fakeImporter) ImportReport(_ context.Context, _, _, _ string, book brokerreport.Workbook) (store.ReportImportResult, error) {
	f.calls++
	if len(book.Sheets) == 0 {
		return store.ReportImportResult{}, io.ErrUnexpectedEOF
	}
	return f.result, nil
}
func (f *fakeImporter) RecordReportFailure(context.Context, string, string, string, error) (store.ReportImportResult, error) {
	return store.ReportImportResult{Status: "failed"}, nil
}

func TestUploadReportMultipartAndDuplicateStatus(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "broker-report-2025-06-01-2025-12-31.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	importer := &fakeImporter{result: store.ReportImportResult{ImportID: 7, Status: "completed"}}
	handler := NewHandler(importer)
	for _, test := range []struct {
		name      string
		duplicate bool
		want      int
	}{{"created", false, http.StatusCreated}, {"duplicate", true, http.StatusOK}} {
		t.Run(test.name, func(t *testing.T) {
			importer.result.Duplicate = test.duplicate
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)
			part, err := writer.CreateFormFile("file", "report.xlsx")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/reports", body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var result store.ReportImportResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.ImportID != 7 || result.Duplicate != test.duplicate {
				t.Fatalf("result=%+v", result)
			}
		})
	}
	if importer.calls != 2 {
		t.Fatalf("import calls=%d, want 2", importer.calls)
	}
}

func TestUploadRejectsWrongMethodAndUnsupportedContent(t *testing.T) {
	handler := NewHandler(&fakeImporter{})
	for _, test := range []struct {
		method, contentType string
		want                int
	}{{http.MethodGet, "", http.StatusMethodNotAllowed}, {http.MethodPost, "application/json", http.StatusUnsupportedMediaType}} {
		req := httptest.NewRequest(test.method, "/api/v1/reports", bytes.NewBufferString("{}"))
		req.Header.Set("Content-Type", test.contentType)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Fatalf("%s: got %d want %d", test.method, rec.Code, test.want)
		}
	}
}

func TestUploadRequiresExactlyOneFile(t *testing.T) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, name := range []string{"one.xlsx", "two.xlsx"} {
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("not an xlsx")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reports", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	NewHandler(&fakeImporter{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
