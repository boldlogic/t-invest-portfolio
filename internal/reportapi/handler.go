package reportapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"t-invest-portfolio/internal/brokerreport"
	"t-invest-portfolio/internal/store"
)

const MaxFileSize = 10 << 20

type Importer interface {
	ImportReport(context.Context, string, string, string, brokerreport.Workbook) (store.ReportImportResult, error)
	RecordReportFailure(context.Context, string, string, string, error) (store.ReportImportResult, error)
}

func NewHandler(importer Importer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/reports", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "поддерживается только POST", http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data;") {
			http.Error(w, "ожидается multipart/form-data", http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxFileSize+(1<<20))
		if err := r.ParseMultipartForm(MaxFileSize); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "размер запроса превышает лимит 10 МиБ", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "не удалось прочитать multipart-запрос", http.StatusBadRequest)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		fileCount := 0
		if r.MultipartForm != nil {
			for _, files := range r.MultipartForm.File {
				fileCount += len(files)
			}
		}
		if fileCount != 1 || r.MultipartForm == nil || len(r.MultipartForm.File["file"]) != 1 {
			http.Error(w, "в запросе должен быть ровно один файл в поле file", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "в multipart-запросе отсутствует поле file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, MaxFileSize+1))
		if err != nil {
			http.Error(w, "не удалось прочитать файл отчёта", http.StatusBadRequest)
			return
		}
		if len(data) == 0 {
			http.Error(w, "файл отчёта пуст", http.StatusBadRequest)
			return
		}
		if len(data) > MaxFileSize {
			http.Error(w, "размер файла превышает 10 МиБ", http.StatusRequestEntityTooLarge)
			return
		}
		filename := filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
		if filename == "." || filename == "" {
			filename = "report.xlsx"
		}
		digest := sha256.Sum256(data)
		hash := hex.EncodeToString(digest[:])
		format := "xlsx"
		book, err := brokerreport.ReadWorkbook(data)
		if err != nil {
			result, saveErr := importer.RecordReportFailure(r.Context(), filename, hash, format, err)
			if saveErr != nil {
				http.Error(w, "не удалось сохранить ошибку разбора отчёта", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusUnprocessableEntity, result)
			return
		}
		result, err := importer.ImportReport(r.Context(), filename, hash, format, book)
		if err != nil {
			if errors.Is(err, store.ErrReportAccountMapping) {
				writeJSON(w, http.StatusConflict, result)
				return
			}
			if errors.Is(err, brokerreport.ErrInvalidReport) {
				writeJSON(w, http.StatusUnprocessableEntity, result)
				return
			}
			http.Error(w, "не удалось обработать отчёт", http.StatusInternalServerError)
			return
		}
		status := http.StatusCreated
		if result.Duplicate {
			status = http.StatusOK
		}
		writeJSON(w, status, result)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, result store.ReportImportResult) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(result); err != nil {
		fmt.Fprintln(w, "не удалось сформировать ответ")
	}
}
