#!/bin/sh
# Fetches the worker's local AI models (#66, docs/decisions/005-modeles-locaux.md)
# into <prefix>:
#
#   worker/models/fetch.sh <prefix>
#
# The same script fills the worker image (a build stage of worker/Dockerfile,
# into /opt/models), the tests on a Mac (`make models`, into .tools/models)
# and CI (cached until this script changes). Each file comes from a pinned
# revision and must match its SHA-256, or the script fails: a file changed
# upstream never gets into the image. The box downloads nothing at run time.
#
#   arctic-embed-m-v2.0       text embeddings, Snowflake, Apache-2.0
#   siglip2-base-patch16-224  image–text embeddings, Google, Apache-2.0
#   pp-ocrv5-latin            OCR, PaddleOCR (ONNX by RapidOCR), Apache-2.0
set -eu

prefix=${1:?usage: fetch.sh <prefix>}
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$prefix"

ARCTIC=https://huggingface.co/Snowflake/snowflake-arctic-embed-m-v2.0/resolve/95c2741480856aa9666782eb4afe11959938017f
SIGLIP=https://huggingface.co/onnx-community/siglip2-base-patch16-224-ONNX/resolve/ba1f3b0843f24bc5417d38e19c37b287d719b2f4
PPOCR=https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.10.0/onnx/PP-OCRv5

# <SHA-256> <file under prefix> <URL>
FILES="
03d923bb1850ebdccb068e2f3abd8aa43fe81c50d07d037ef103fe3d0fb78e3b arctic-embed-m-v2.0/model.onnx $ARCTIC/onnx/model_quantized.onnx
f1cc44ad7faaeec47241864835473fd5403f2da94673f3f764a77ebcb0a803ec arctic-embed-m-v2.0/tokenizer.json $ARCTIC/tokenizer.json
5f2b401c1a4fc095702a5d45348e17ad46c4f87064085365b43c6e8eaa5c0070 siglip2-base-patch16-224/vision_model.onnx $SIGLIP/onnx/vision_model_quantized.onnx
3a0603d3a00c05a80a6ded4743c16aaac7b1e62cdcc7e362e7ce418659b96400 siglip2-base-patch16-224/text_model.onnx $SIGLIP/onnx/text_model_quantized.onnx
cb9140fae3ac5122c972d37adf83e1248471a38147ad76f8215c8872c6fd8322 siglip2-base-patch16-224/tokenizer.json $SIGLIP/tokenizer.json
4d97c44a20d30a81aad087d6a396b08f786c4635742afc391f6621f5c6ae78ae pp-ocrv5-latin/det.onnx $PPOCR/det/ch_PP-OCRv5_det_mobile.onnx
b20bd37c168a570f583afbc8cd7925603890efbcdc000a59e22c269d160b5f5a pp-ocrv5-latin/rec.onnx $PPOCR/rec/latin_PP-OCRv5_rec_mobile.onnx
"

sha256() {
	if command -v sha256sum >/dev/null; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

sums=$(mktemp)
trap 'rm -f "$sums"' EXIT
echo "$FILES" | while read -r sum path url; do
	[ -n "$sum" ] || continue
	dest=$prefix/$path
	if [ ! -f "$dest" ] || [ "$(sha256 "$dest")" != "$sum" ]; then
		mkdir -p "$(dirname "$dest")"
		# A dropped connection resumes where it stopped (-C -).
		curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors -C - -o "$dest.part" "$url"
		got=$(sha256 "$dest.part")
		if [ "$got" != "$sum" ]; then
			rm -f "$dest.part"
			echo "$path: SHA-256 $got, expected $sum ($url)" >&2
			exit 1
		fi
		mv "$dest.part" "$dest"
	fi
	echo "$sum  $path" >>"$sums"
done

# The licences travel with the models. SHA256SUMS comes last: its presence
# says the folder is complete (`make models` waits for it).
cp "$here/NOTICE.md" "$here/LICENSE-Apache-2.0.txt" "$prefix/"
mv "$sums" "$prefix/SHA256SUMS"
trap - EXIT
chmod 644 "$prefix/SHA256SUMS"
cat "$prefix/SHA256SUMS"
