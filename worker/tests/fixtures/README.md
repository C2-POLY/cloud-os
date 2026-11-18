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
