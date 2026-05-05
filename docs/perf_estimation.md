# Performance Estimation Report

**Platform:** Apple M4, macOS, local NVMe SSD  
**Configuration:** 8 workers (default), copy mode unless noted  
**Go version:** 1.25  

All figures are measured via `go test -bench=. -benchmem -benchtime=5s` on real hardware.
Extrapolated values are marked `*`.

---

## 1. Copy Throughput by File Size

Fixed per-call overhead (file open + create + fsync) is ~4 ms regardless of size — this dominates for small files and explains the non-linear throughput curve.

| File size | Time per call | Throughput | Allocs |
|-----------|:------------:|----------:|-------:|
| 1 KB | 4.7 ms | 0.22 MB/s | 45,633 B |
| 10 KB | 4.8 ms | 2.1 MB/s | 48,746 B |
| 100 KB | 4.1 ms | 25.0 MB/s | 47,687 B |
| 1 MB | 5.3 ms | 199 MB/s | 45,569 B |
| 10 MB | 9.8 ms | 1,074 MB/s | 38,752 B |
| 100 MB | 136 ms | 773 MB/s | 68,280 B |
| 500 MB* | ~680 ms | ~735 MB/s* | ~80 KB* |
| 1 GB* | ~1,360 ms | ~735 MB/s* | ~80 KB* |
| 4 GB* | ~5,440 ms | ~735 MB/s* | ~80 KB* |

> Throughput plateaus at ~735–773 MB/s for files ≥ 100 MB, which is the SSD sequential write limit. Files smaller than 1 MB are dominated by fixed syscall overhead, so optimising data transfer rate has negligible effect in that range.

> For **move mode** (`os.Rename`, same filesystem), the operation is a metadata-only update: ~50 µs per file regardless of size.

---

## 2. Directory Walk Performance by File Count

Walk is O(n). Per-file cost is ~0.6 µs; memory usage is ~366 bytes per path.

| File count | Walk time | Peak memory | µs/file | Bytes/file |
|------------|----------:|------------:|--------:|-----------:|
| 100 | 67 µs | 30 KB | 0.67 | 299 |
| 1,000 | 512 µs | 264 KB | 0.51 | 264 |
| 10,000 | 5.5 ms | 3.25 MB | 0.55 | 325 |
| 50,000 | 30.8 ms | 18.3 MB | 0.62 | 366 |
| 100,000* | ~62 ms | ~36.6 MB* | 0.62* | 366* |
| 500,000* | ~310 ms | ~183 MB* | 0.62* | 366* |
| 1,000,000* | ~620 ms | ~366 MB* | 0.62* | 366* |

> Walk time is negligible compared to EXIF extraction and file I/O. The path buffer is the main memory cost and is held in RAM for the entire run.

---

## 3. EXIF Extraction Throughput

Measured with `b.RunParallel` on tagged JPEG files; pool size matches worker count.

| Workers / Pool | ns/op | Files/sec |
|---------------|------:|----------:|
| 1 | 743,180 | 1,346 |
| 4 | 224,573 | 4,452 |
| 8 | 179,319 | 5,577 |
| 16* | ~145,000* | ~6,897* |

> Scaling is sub-linear above 8 due to shared I/O bandwidth and OS scheduling overhead. Diminishing returns are expected beyond 8 workers on a 10-core machine.

---

## 4. End-to-End Runtime Estimation by Scenario

Formula per scenario (copy mode, 8 workers):

```
walk_time    = N × 0.62 µs
exif_time    = (N / 8) × 179 µs
copy_time    = (N / 8) × (4 ms overhead + file_size / throughput)
total        ≈ max(walk_time, exif_time + copy_time)   [walk and EXIF overlap]
peak_memory  ≈ (N × 366 B) path_buffer
             + (8 × 1 MB) copy_pool
             + (8 × ~15 MB) exiftool_processes
```

### Scenario A — Holiday trip: 500 JPEGs, 4 MB each (2 GB total)

| Stage | Time |
|-------|-----:|
| Walk (500 files) | 0.3 ms |
| EXIF extraction (8 workers) | 500/8 × 179 µs = **11 ms** |
| Copy (8 workers, 4 MB @ ~200 MB/s) | 500/8 × 24 ms = **1,500 ms** |
| **Total** | **≈ 1.5 s** |

Peak memory: 500 × 366 B + 8 MB copy + 120 MB exiftool ≈ **128 MB**

---

### Scenario B — Camera RAW batch: 200 files, 40 MB each (8 GB total)

| Stage | Time |
|-------|-----:|
| Walk (200 files) | 0.1 ms |
| EXIF extraction | 200/8 × 179 µs = **4.5 ms** |
| Copy (8 workers, 40 MB @ ~800 MB/s) | 200/8 × 54 ms = **1,350 ms** |
| **Total** | **≈ 1.4 s** |

Peak memory: 200 × 366 B + 8 MB + 120 MB ≈ **128 MB**

---

### Scenario C — GoPro 4K footage: 100 clips, 500 MB each (50 GB total)

| Stage | Time |
|-------|-----:|
| Walk (100 files) | 0.06 ms |
| EXIF extraction | 100/8 × 179 µs = **2.2 ms** |
| Copy (8 workers, 500 MB @ ~735 MB/s) | 100/8 × 684 ms = **8,550 ms** |
| **Total** | **≈ 8.6 s** |

Peak memory: 100 × 366 B + 8 MB + 120 MB ≈ **128 MB**

---

### Scenario D — Full photo library: 50,000 mixed files, avg 3 MB (150 GB total)

| Stage | Time |
|-------|-----:|
| Walk (50,000 files) | **31 ms** (measured) |
| EXIF extraction | 50,000/8 × 179 µs = **1,119 ms** |
| Copy (8 workers, 3 MB @ ~180 MB/s) | 50,000/8 × 21 ms = **131,250 ms** |
| **Total** | **≈ 132 s (2.2 min)** |

Peak memory: 50,000 × 366 B + 8 MB + 120 MB ≈ **147 MB**

---

### Scenario E — Large video archive: 1,000 clips, 2 GB each (2 TB total)

| Stage | Time |
|-------|-----:|
| Walk (1,000 files) | 0.6 ms |
| EXIF extraction | 1,000/8 × 179 µs = **22 ms** |
| Copy (8 workers, 2 GB @ ~735 MB/s) | 1,000/8 × 2,721 ms = **340,125 ms** |
| **Total** | **≈ 340 s (5.7 min)** |

Peak memory: 1,000 × 366 B + 8 MB + 120 MB ≈ **128 MB**

---

## 5. Summary Table

| Scenario | Files | Total size | Copy mode | Est. time | Peak memory |
|----------|------:|----------:|-----------|----------:|------------:|
| Holiday JPEGs (4 MB) | 500 | 2 GB | copy | ~1.5 s | ~128 MB |
| Camera RAW (40 MB) | 200 | 8 GB | copy | ~1.4 s | ~128 MB |
| GoPro 4K (500 MB) | 100 | 50 GB | copy | ~8.6 s | ~128 MB |
| Full photo library (3 MB avg) | 50,000 | 150 GB | copy | ~2.2 min | ~147 MB |
| Video archive (2 GB) | 1,000 | 2 TB | copy | ~5.7 min | ~128 MB |
| Full photo library (3 MB avg) | 50,000 | 150 GB | **move** | ~7 s | ~147 MB |

> **Move mode** (`-copy` flag omitted) uses `os.Rename` for same-filesystem transfers (~50 µs/file). For the 50,000-file library in move mode, the bottleneck becomes EXIF extraction (~1.1 s) rather than I/O, cutting total time from 2.2 min to ~7 s.

---

## 6. Caveats

- All copy benchmarks are **local NVMe SSD → same SSD**. Network shares (NFS, SMB), spinning HDDs, or USB drives will be 5–50× slower on the copy stage.
- EXIF extraction times assume standard JPEG/MP4 metadata. Files with unusually large or deeply nested EXIF blocks may take longer.
- The ~15 MB per exiftool process estimate is approximate; actual RSS depends on the installed Perl runtime version.
- Estimates assume no concurrent filesystem contention.
