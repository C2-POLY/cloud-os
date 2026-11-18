-- +goose Up
-- Text read by OCR from photographed or scanned documents (#68). An image gets
-- a row of its own (source 'image'); a PDF without a text layer keeps the row
-- of #67, whose text the OCR fills in. ocr_model names the model that read the
-- text, NULL when the text comes from the file itself. A plain photo, not a
-- document, gets a row without text and with ocr_model set: it is looked at once.
ALTER TABLE document_text DROP CONSTRAINT document_text_source_check;
ALTER TABLE document_text ADD CONSTRAINT document_text_source_check
    CHECK (source IN ('pdf', 'docx', 'xlsx', 'pptx', 'odt', 'ods', 'odp', 'text', 'image'));
ALTER TABLE document_text ADD COLUMN ocr_model text;
ALTER TABLE document_text ADD CONSTRAINT document_text_image_ocr
    CHECK (source <> 'image' OR ocr_model IS NOT NULL);

-- +goose Down
-- Only derived rows go: an image has no text before #68, and `core text
-- backfill` reads a scanned PDF again, without text as before.
DELETE FROM document_text WHERE ocr_model IS NOT NULL;
ALTER TABLE document_text DROP CONSTRAINT document_text_image_ocr;
ALTER TABLE document_text DROP COLUMN ocr_model;
ALTER TABLE document_text DROP CONSTRAINT document_text_source_check;
ALTER TABLE document_text ADD CONSTRAINT document_text_source_check
    CHECK (source IN ('pdf', 'docx', 'xlsx', 'pptx', 'odt', 'ods', 'odp', 'text'));
