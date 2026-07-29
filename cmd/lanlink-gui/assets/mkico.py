"""从 PNG 生成高质量多分辨率 Windows ICO 文件。"""
from PIL import Image
import struct
import io
import sys


def png_to_ico(png_path, ico_path, sizes=(16, 24, 32, 48, 64, 96, 128, 256, 512)):
    src = Image.open(png_path).convert("RGBA")
    frames = []
    for size in sizes:
        img = src.resize((size, size), Image.LANCZOS)
        # 256x256 及以上用 PNG 压缩（Windows 支持）
        if size >= 256:
            buf = io.BytesIO()
            img.save(buf, format="PNG")
            data = buf.getvalue()
        else:
            # 小尺寸用 BMP 格式（无文件头）
            data = bmp_rgba_data(img)
        frames.append((size, data))

    # 写入 ICO 文件
    count = len(frames)
    header = struct.pack("<HHH", 0, 1, count)  # reserved, type(1=icon), count

    # 计算目录和数据的偏移
    dir_size = 16 * count
    data_offset = 6 + dir_size
    directories = b""
    data_all = b""

    for size, data in frames:
        w = size if size < 256 else 0
        h = size if size < 256 else 0
        # BMP 数据需要把高度翻倍（DIB 格式要求）
        if size < 256:
            # 对于 BMP 数据，DIB header 里的高度已经是 2*size
            # 但我们的 bmp_rgba_data 已经处理了，这里 size 就是数据大小
            pass
        dir_entry = struct.pack("<BBBBHHII", w, h, 0, 0, 1, 32, len(data), data_offset)
        directories += dir_entry
        data_all += data
        data_offset += len(data)

    with open(ico_path, "wb") as f:
        f.write(header)
        f.write(directories)
        f.write(data_all)

    print(f"ICO saved: {ico_path} ({count} frames, {sum(len(d) for _, d in frames)} bytes)")


def bmp_rgba_data(img):
    """生成无文件头的 32-bit BMP DIB 数据（含 BITMAPINFOHEADER）。"""
    w, h = img.size
    # DIB header (BITMAPINFOHEADER)
    header_size = 40
    row_size = ((w * 4 + 3) // 4) * 4  # 每行 4 字节对齐
    image_size = row_size * h
    dib = struct.pack(
        "<IIIHHIIIIII",
        header_size,   # biSize
        w,             # biWidth
        h * 2,         # biHeight (2x for XOR + AND masks, but we use 32bpp so AND is empty)
        1,             # biPlanes
        32,            # biBitCount
        0,             # biCompression (BI_RGB)
        image_size,    # biSizeImage
        0, 0, 0, 0     # biXPelsPerMeter, biYPelsPerMeter, biClrUsed, biClrImportant
    )

    # 像素数据（BGRA，自下而上）
    pixels = img.tobytes("raw", "BGRA")
    rows = []
    for y in range(h - 1, -1, -1):
        row = pixels[y * w * 4:(y + 1) * w * 4]
        padding = b"\x00" * (row_size - w * 4)
        rows.append(row + padding)

    return dib + b"".join(rows)


if __name__ == "__main__":
    png_to_ico(sys.argv[1], sys.argv[2])
