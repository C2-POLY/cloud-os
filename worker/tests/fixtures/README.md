# Test fixtures

- `photo.heic`: 64×48 HEIC (HEVC, `ftypheic`), 1.9 KB, made for these tests from
  a synthetic image (a red rectangle and a yellow disc on blue, drawn with
  Pillow) and encoded with macOS `sips`. Its EXIF: Apple, iPhone 15, taken
  2024-07-14 18:03:22.123 at +02:00, GPS 48°51'29.6" N 2°17'40.2" E. No
  photograph and no personal data: free to use, under the project's licence.
- `clip.mp4`: 320×240 MPEG-4 Part 2 video, 2 s at 10 fps, 45 KB, made for these
  tests from ffmpeg's `testsrc` pattern (no camera, no sound). Tags:
  `creation_time` 2024-07-14T16:03:22Z, `location` +48.8584+002.2945/.
- `iphone.mov`: 320×240 QuickTime video, 0.5 s, 14 KB, from the same pattern,
  with the tags an iPhone writes (`com.apple.quicktime.creationdate`
  2024-07-14T18:03:22+0200, `…location.ISO6709`, `…make` Apple, `…model`
  iPhone 15), and its track header's display matrix set to a 90° rotation, so
  it plays upright as 240×320, as a phone held upright records it.
  Both were encoded with ffmpeg in a throwaway container; free to use, under
  the project's licence.

## Documents (text extraction, #67)

All their text is invented for these tests (a lease for a flat "rue des
Tilleuls à Nantes", a household budget, a moving plan, a plumber's invoice):
no personal data, no third-party content; free to use, under the project's
licence. French with accents on purpose (é, è, ê, ô, É, €).

- `letter.pdf`: 2 pages, 26 KB, a text layer with embedded fonts. An HTML
  page printed by headless Chrome (`--print-to-pdf --no-pdf-header-footer`,
  Skia/PDF); page 2 starts at "État des lieux".
- `letter.docx`, `letter.odt`: the same HTML page converted by macOS
  `textutil -strip -author "" -convert docx|odt` (Cocoa writers, no author).
  `textutil` writes the HTML table as plain paragraphs in the docx and as a
  real table in the odt.
- `budget.xlsx`: two sheets, "Budget 2024" (text in shared strings, numbers
  850, 62.5, 18) and "Notes", made with openpyxl 3.1.5 (MIT).
- `slides.pptx`: two slides (title and body), made with python-pptx 1.0.2
  (MIT) from its default template.
- `budget.ods`, `slides.odp`: the same spreadsheet and slides in
  OpenDocument, made with odfpy 1.4.1 (Apache-2.0).
- `scan.pdf`: one A4 page at 75 dpi, an image without any text layer, as a
  scanner makes it: the invoice's lines drawn with Pillow's default font,
  saved as PDF by Pillow (JPEG inside), 16 KB. That font has no euro sign:
  the page shows an empty box after "128,40", which the OCR rightly skips.

## Photographed and scanned documents (OCR, #68)

- `invoice-scan.jpg` (1240×1754, 229 KB) and `invoice-photo.jpg` (992×1403,
  66 KB): the benchmark's electricity invoice,
  `bench/docs/facture-electricite-mars.txt` (invented, see
  `bench/README.md`), drawn on an A4 page at 150 dpi with DejaVu Sans by
  `worker/bench/ocr.py` (`render`), then made into a flatbed scan
  (`as_scan`: a slight tilt, blur, grain, JPEG at 70) and into a phone photo
  of the page (`as_photo`: perspective, uneven light, blur, lower resolution,
  JPEG at 80), with the random generator seeded by the document's name. Free
  to use, under the project's licence.

openpyxl, python-pptx and odfpy were only used once, in a throwaway
environment, to make these files: they are not dependencies of the worker.
- `invoice-en-photo.jpg` (992×1403, 53 KB): an invented English invoice,
  `invoice-en.txt` (no real company, person or address), drawn and
  photographed the same way (`as_photo`, seeded by "invoice-en"). Free to
  use, under the project's licence.
