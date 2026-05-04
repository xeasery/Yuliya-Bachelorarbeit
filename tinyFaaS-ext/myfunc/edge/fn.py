import base64
import numpy as np

def fn(*args):
    try:
        if len(args) == 0 or args[0] is None:
            return "ERROR: no input"

        size = 256

        # decode base64 → bytes
        img_bytes = base64.b64decode(args[0])

        # convert to numpy
        img = np.frombuffer(img_bytes, dtype=np.uint8)

        # enforce fixed size (THIS fixes your error)
        expected = size * size

        if len(img) < expected:
            img = np.pad(img, (0, expected - len(img)))
        else:
            img = img[:expected]

        img = img.reshape((size, size))

        # simple edge detection (Sobel-like)
        Kx = np.array([[-1,0,1],[-2,0,2],[-1,0,1]])
        Ky = np.array([[-1,-2,-1],[0,0,0],[1,2,1]])

        def conv(image, kernel):
            h, w = image.shape
            out = np.zeros_like(image)
            for i in range(1, h-1):
                for j in range(1, w-1):
                    region = image[i-1:i+2, j-1:j+2]
                    out[i,j] = np.sum(region * kernel)
            return out

        gx = conv(img, Kx)
        gy = conv(img, Ky)

        edges = np.sqrt(gx**2 + gy**2)
        edges = (edges / (edges.max() + 1e-9) * 255).astype(np.uint8)

        # return RAW bytes → base64
        return base64.b64encode(edges.tobytes()).decode()

    except Exception as e:
        return f"ERROR: {str(e)}"
