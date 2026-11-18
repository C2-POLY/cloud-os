# Local AI models of the Cloud OS worker

Fetched by `worker/models/fetch.sh` at a pinned revision, checked against their SHA-256, and redistributed unmodified. Why these models: `docs/decisions/005-modeles-locaux.md`. Each is under the Apache License, Version 2.0 (`LICENSE-Apache-2.0.txt`, next to this file).

| Folder | Model | Source | Licence |
|---|---|---|---|
| `arctic-embed-m-v2.0/` | Snowflake Arctic Embed M v2.0, fp16 ONNX, © Snowflake Inc. | <https://huggingface.co/Snowflake/snowflake-arctic-embed-m-v2.0> (revision `95c2741480856aa9666782eb4afe11959938017f`) | Apache-2.0 |
| `siglip2-base-patch16-224/` | SigLIP 2 base/16 at 224 px, © Google LLC; int8 ONNX by Hugging Face (onnx-community) | <https://huggingface.co/google/siglip2-base-patch16-224>, ONNX: <https://huggingface.co/onnx-community/siglip2-base-patch16-224-ONNX> (revision `ba1f3b0843f24bc5417d38e19c37b287d719b2f4`) | Apache-2.0 |
| `pp-ocrv5-latin/` | PP-OCRv5 mobile text detector and Latin mobile text recogniser, © PaddlePaddle Authors (Baidu); ONNX by the RapidOCR Authors | <https://github.com/PaddlePaddle/PaddleOCR>, ONNX: <https://www.modelscope.cn/models/RapidAI/RapidOCR> (release `v3.10.0`) | Apache-2.0 |
