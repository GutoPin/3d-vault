"""Pruebas del núcleo del complemento de Blender, sin Blender.

Uso: make test-blender (necesita Python con numpy).
"""

import hashlib
import io
import os
import random
import struct
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "three_d_vault"))
import vault  # noqa: E402

# Misma huella que TestGoldenCutsWeb (Go) y web/sync_test.mjs.
GOLDEN = "914a77e6310914426999f20244aa1536d24dcba473db4056e9be2b321b537ff9"


def hash_data(size):
    out = bytearray()
    i = 0
    while len(out) < size:
        out += hashlib.sha256(struct.pack("<I", i)).digest()
        i += 1
    return bytes(out[:size])


def reference_cuts(data):
    """FastCDC byte a byte, traducción directa de internal/chunker."""
    cuts, start = [], 0
    while start < len(data):
        n = len(data) - start
        if n <= vault.MIN_SIZE:
            cuts.append(n)
            break
        end, fp, i, cut = min(n, vault.MAX_SIZE), 0, vault.MIN_SIZE, None
        while i < end:
            fp = ((fp << 1) + vault.GEAR[data[start + i]]) & 0xFFFFFFFF
            if fp & (vault.MASK_SMALL if i < vault.AVG_SIZE else vault.MASK_LARGE) == 0:
                cut = i + 1
                break
            i += 1
        cuts.append(cut or end)
        start += cuts[-1]
    return cuts


class ChunkerTest(unittest.TestCase):
    def test_golden_cuts(self):
        fp = hashlib.sha256()
        for off, data in vault.chunks(io.BytesIO(hash_data(40 << 20))):
            fp.update(f"{off}:{len(data)}:{hashlib.sha256(data).hexdigest()}\n".encode())
        self.assertEqual(fp.hexdigest(), GOLDEN)

    def test_matches_byte_by_byte(self):
        rng = random.Random(7)
        # Datos poco aleatorios fuerzan cortes en el máximo y cerca de los bordes.
        for data in (rng.randbytes(9 << 20), bytes(9 << 20), rng.randbytes(300 << 10), b"x"):
            got = [len(d) for _, d in vault.chunks(io.BytesIO(data))]
            self.assertEqual(got, reference_cuts(data))

    def test_small_reads_give_same_cuts(self):
        data = random.Random(3).randbytes(20 << 20)
        whole = [len(d) for _, d in vault.chunks(io.BytesIO(data))]
        old = vault.READ_BLOCK
        vault.READ_BLOCK = 100_003
        try:
            parts = [len(d) for _, d in vault.chunks(io.BytesIO(data))]
        finally:
            vault.READ_BLOCK = old
        self.assertEqual(parts, whole)


class HelpersTest(unittest.TestCase):
    def test_code_round_trip(self):
        import base64
        import json
        raw = json.dumps({"s": "https://x.example/", "t": "tok", "c": "id", "n": "PC de Ana"}).encode()
        code = "3DV1:" + base64.urlsafe_b64encode(raw).decode().rstrip("=")
        self.assertEqual(vault.parse_code(code), {"server": "https://x.example", "token": "tok", "client_id": "id", "client_name": "PC de Ana"})
        with self.assertRaises(vault.VaultError):
            vault.parse_code("hola")

    def test_locate(self):
        projects = [{"id": 1, "name": "Camiones"}, {"id": 2, "name": "Mina: Norte"}]
        root = tempfile.gettempdir()
        p, path = vault.locate(os.path.join(root, "3D-Vault", "camiones", "modelos", "a.blend"), projects)
        self.assertEqual((p["id"], path), (1, "modelos/a.blend"))
        p, path = vault.locate(os.path.join(root, "3D-Vault", "Mina_ Norte", "b.blend"), projects)
        self.assertEqual((p["id"], path), (2, "b.blend"))
        self.assertEqual(vault.locate(os.path.join(root, "otro", "c.blend"), projects), (None, None))


if __name__ == "__main__":
    unittest.main()
