package brokerreport

import (
	"bytes"
	"fmt"

	"github.com/xuri/excelize/v2"
)

type Sheet struct {
	Name string
	Rows [][]string
}

type Workbook struct{ Sheets []Sheet }

func ReadWorkbook(data []byte) (Workbook, error) {
	if len(data) < 4 || !bytes.Equal(data[:2], []byte{'P', 'K'}) {
		return Workbook{}, fmt.Errorf("поддерживаются только файлы XLSX")
	}
	wb, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return Workbook{}, fmt.Errorf("открыть XLSX: %w", err)
	}
	defer wb.Close()
	out := Workbook{}
	for _, name := range wb.GetSheetList() {
		rows, err := wb.GetRows(name, excelize.Options{RawCellValue: true})
		if err != nil {
			return Workbook{}, fmt.Errorf("прочитать лист %q: %w", name, err)
		}
		out.Sheets = append(out.Sheets, Sheet{Name: name, Rows: rows})
	}
	if len(out.Sheets) == 0 {
		return Workbook{}, fmt.Errorf("в книге нет листов")
	}
	return out, nil
}
