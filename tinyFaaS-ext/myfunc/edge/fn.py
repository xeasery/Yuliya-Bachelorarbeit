import base64
import io

import numpy as np
from PIL import Image

# cap dimensions so a large upload can't blow up memory/CPU on a constrained
# edge device
MAX_SIZE = 512


def fn(*args):
    try:
        if len(args) == 0 or args[0] is None:
            return "ERROR: no input"

        # input is a base64-encoded image file (jpg/png/...), not raw pixels
        img_bytes = base64.b64decode(args[0])
        img = Image.open(io.BytesIO(img_bytes)).convert("L")

        if img.width > MAX_SIZE or img.height > MAX_SIZE:
            img.thumbnail((MAX_SIZE, MAX_SIZE))

        pixels = np.asarray(img, dtype=np.float64)

        # Sobel operator, vectorized over the whole image (no per-pixel
        # Python loop -- the original nested-loop version took seconds per
        # image and didn't scale past small sizes)
        gx = np.zeros_like(pixels)
        gy = np.zeros_like(pixels)

        gx[1:-1, 1:-1] = (
            -pixels[:-2, :-2] + pixels[:-2, 2:]
            - 2 * pixels[1:-1, :-2] + 2 * pixels[1:-1, 2:]
            - pixels[2:, :-2] + pixels[2:, 2:]
        )
        gy[1:-1, 1:-1] = (
            -pixels[:-2, :-2] - 2 * pixels[:-2, 1:-1] - pixels[:-2, 2:]
            + pixels[2:, :-2] + 2 * pixels[2:, 1:-1] + pixels[2:, 2:]
        )

        edges = np.sqrt(gx ** 2 + gy ** 2)
        edges = (edges / (edges.max() + 1e-9) * 255).astype(np.uint8)

        # return a real PNG, base64-encoded, so the caller can decode it
        # straight into a viewable image without knowing width/height/mode
        out = io.BytesIO()
        Image.fromarray(edges, mode="L").save(out, format="PNG")
        return base64.b64encode(out.getvalue()).decode()

    except Exception as e:
        return f"ERROR: {str(e)}"
