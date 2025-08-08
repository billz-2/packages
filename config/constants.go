package config

const (
	DateTimeFormat = "2006-01-02 15:04:05"
)

const (
	BillzOFDInternalError                        = "INTERNAL"
	BillzOFDValidationErrNoItemsCode             = "NO_ITEMS"
	BillzOFDValidationErrVatSumIncorrectCode     = "VAT_SUM_INCORRECT"
	BillzOFDValidationErrVatItemSumIncorrectCode = "VAT_ITEM_SUM_INCORRECT"
	BillzOFDValidationErrTotalSumIncorrectCode   = "TOTAL_SUM_INCORRECT"
	BillzOFDValidationErrInvalidItemDataCode     = "INVALID_ITEM_DATA"
	BillzOFDValidationErrInvalidPosItem          = "INVALID_POS_ITEM"
	BillzOFDValidationErrNoPrinterCode           = "NO_PRINTER"
	BillzOFDValidationErrDuplicateMarkingCode    = "DUPLICATE_MARKING_CODE"
	BillzOFDValidationErrExistingMarkingCode     = "EXISTING_MARKING_CODE"
)
