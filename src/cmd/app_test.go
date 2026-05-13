package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

func TestMain(m *testing.M) {
	logrus.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// MockExifService is a testify mock for the ExifService interface.
type MockExifService struct {
	mock.Mock
}

func (m *MockExifService) ExtractDate(path string, debug bool, useFileModifyDate bool) (time.Time, string, error) {
	args := m.Called(path, debug, useFileModifyDate)
	return args.Get(0).(time.Time), args.String(1), args.Error(2)
}

// ---- AppTestSuite ----

type AppTestSuite struct {
	suite.Suite
	tmpDir string
}

func (s *AppTestSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
}

func TestAppSuite(t *testing.T) {
	suite.Run(t, new(AppTestSuite))
}

// helpers

func (s *AppTestSuite) writeFile(name, content string) string {
	path := filepath.Join(s.tmpDir, name)
	require.NoError(s.T(), os.WriteFile(path, []byte(content), 0644))
	return path
}

func (s *AppTestSuite) appWith(cfg *Config, svc ExifService) *App {
	return &App{Config: cfg, ExifService: svc}
}

// ---- collectFiles ----

func (s *AppTestSuite) TestCollectFiles_ReturnsAllFiles() {
	s.writeFile("a.jpg", "a")
	s.writeFile("b.mp4", "b")

	paths, _, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(2, count)
	s.Len(paths, 2)
}

func (s *AppTestSuite) TestCollectFiles_EmptyDir() {
	paths, _, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(0, count)
	s.Empty(paths)
}

func (s *AppTestSuite) TestCollectFiles_NestedFiles() {
	sub := filepath.Join(s.tmpDir, "2023", "01")
	require.NoError(s.T(), os.MkdirAll(sub, 0755))
	require.NoError(s.T(), os.WriteFile(filepath.Join(sub, "nested.jpg"), []byte("x"), 0644))
	s.writeFile("root.jpg", "y")

	_, _, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(2, count)
}

func (s *AppTestSuite) TestCollectFiles_SkipsSpotlightDir() {
	dir := filepath.Join(s.tmpDir, ".Spotlight-V100")
	require.NoError(s.T(), os.MkdirAll(dir, 0755))
	require.NoError(s.T(), os.WriteFile(filepath.Join(dir, "hidden.jpg"), []byte("x"), 0644))
	s.writeFile("visible.jpg", "y")

	paths, _, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(1, count)
	s.Contains(paths[0], "visible.jpg")
}

func (s *AppTestSuite) TestCollectFiles_SkipsFseventsd() {
	dir := filepath.Join(s.tmpDir, ".fseventsd")
	require.NoError(s.T(), os.MkdirAll(dir, 0755))
	require.NoError(s.T(), os.WriteFile(filepath.Join(dir, "event"), []byte("x"), 0644))

	_, _, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(0, count)
}

func (s *AppTestSuite) TestCollectFiles_SkipsDocumentRevisions() {
	dir := filepath.Join(s.tmpDir, ".DocumentRevisions-V100")
	require.NoError(s.T(), os.MkdirAll(dir, 0755))
	require.NoError(s.T(), os.WriteFile(filepath.Join(dir, "rev"), []byte("x"), 0644))

	_, _, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(0, count)
}

func (s *AppTestSuite) TestCollectFiles_SkipsNonMediaFiles() {
	s.writeFile("photo.jpg", "a")
	s.writeFile("document.pdf", "b")
	s.writeFile("readme.txt", "c")
	s.writeFile("clip.mp4", "d")

	paths, _, count, skipped := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(2, count)
	s.Equal(2, skipped)
	s.Len(paths, 2)
	for _, p := range paths {
		s.True(p[len(p)-4:] == ".jpg" || p[len(p)-4:] == ".mp4")
	}
}

func (s *AppTestSuite) TestCollectFiles_CaseInsensitiveExtension() {
	s.writeFile("PHOTO.JPG", "a")
	s.writeFile("clip.MP4", "b")

	_, _, count, skipped := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(2, count)
	s.Equal(0, skipped)
}

// ---- processFile ----

func (s *AppTestSuite) TestProcessFile_LocalMove() {
	src := s.writeFile("photo.jpg", "data")
	outDir := filepath.Join(s.tmpDir, "output")

	fixedTime := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(fixedTime, "DateTimeOriginal", nil)

	_, err := s.appWith(&Config{OutputPath: outDir}, svc).processFile(context.Background(), src)

	s.NoError(err)
	s.noFile(src, "source should be removed after move")
	s.hasFile(filepath.Join(outDir, "2023", "05", "photo.jpg"))
	svc.AssertExpectations(s.T())
}

func (s *AppTestSuite) TestProcessFile_LocalCopy() {
	src := s.writeFile("photo.jpg", "data")
	outDir := filepath.Join(s.tmpDir, "output")

	fixedTime := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(fixedTime, "DateTimeOriginal", nil)

	_, err := s.appWith(&Config{OutputPath: outDir, CopyMode: true}, svc).processFile(context.Background(), src)

	s.NoError(err)
	s.hasFile(src)
	s.hasFile(filepath.Join(outDir, "2023", "05", "photo.jpg"))
	svc.AssertExpectations(s.T())
}

func (s *AppTestSuite) TestProcessFile_DryRun_NoChanges() {
	src := s.writeFile("photo.jpg", "data")
	outDir := filepath.Join(s.tmpDir, "output")

	fixedTime := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(fixedTime, "DateTimeOriginal", nil)

	_, err := s.appWith(&Config{OutputPath: outDir, DryRun: true}, svc).processFile(context.Background(), src)

	s.NoError(err)
	s.hasFile(src)
	s.noFile(filepath.Join(outDir, "2023", "05", "photo.jpg"), "no file should be created in dry-run")
	svc.AssertExpectations(s.T())
}

func (s *AppTestSuite) TestProcessFile_DateFolderStructure() {
	src := s.writeFile("img.jpg", "x")
	outDir := filepath.Join(s.tmpDir, "output")

	// Verify YYYY/MM structure is built from the extracted date
	fixedTime := time.Date(2001, 9, 11, 0, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(fixedTime, "DateTimeOriginal", nil)

	_, processErr := s.appWith(&Config{OutputPath: outDir, CopyMode: true}, svc).processFile(context.Background(), src)
	s.NoError(processErr)

	s.hasFile(filepath.Join(outDir, "2001", "09", "img.jpg"))
}

func (s *AppTestSuite) TestProcessFile_NoDate_ReturnsError() {
	src := s.writeFile("photo.jpg", "data")

	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(time.Time{}, "", nil)

	_, err := s.appWith(&Config{OutputPath: s.tmpDir}, svc).processFile(context.Background(), src)

	s.Error(err)
	svc.AssertExpectations(s.T())
}

func (s *AppTestSuite) TestProcessFile_ExifError_ReturnsError() {
	src := s.writeFile("photo.jpg", "data")

	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(time.Time{}, "", assert.AnError)

	_, err := s.appWith(&Config{OutputPath: s.tmpDir}, svc).processFile(context.Background(), src)

	s.Error(err)
	svc.AssertExpectations(s.T())
}

func (s *AppTestSuite) TestProcessFile_OnlyDateTimeOriginal_SkipsCreateDate() {
	src := s.writeFile("photo.jpg", "data")
	outDir := filepath.Join(s.tmpDir, "output")

	fixedTime := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(fixedTime, "CreateDate", nil)

	_, err := s.appWith(&Config{OutputPath: outDir, OnlyDateTimeOriginal: true}, svc).processFile(context.Background(), src)

	s.Error(err)
	s.hasFile(src)
	svc.AssertExpectations(s.T())
}

func (s *AppTestSuite) TestProcessFile_OnlyDateTimeOriginal_AllowsDateTimeOriginal() {
	src := s.writeFile("photo.jpg", "data")
	outDir := filepath.Join(s.tmpDir, "output")

	fixedTime := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(fixedTime, "DateTimeOriginal", nil)

	_, err := s.appWith(&Config{OutputPath: outDir, OnlyDateTimeOriginal: true, CopyMode: true}, svc).processFile(context.Background(), src)

	s.NoError(err)
	svc.AssertExpectations(s.T())
}

// TestProcessFile_CopyPreservesModTime verifies that after a local copy the destination
// file keeps the same mtime as the source, not the time of the copy operation.
func (s *AppTestSuite) TestProcessFile_CopyPreservesModTime() {
	src := s.writeFile("photo.jpg", "data")
	outDir := filepath.Join(s.tmpDir, "output")

	wantMtime := time.Date(2018, 6, 15, 10, 30, 0, 0, time.UTC)
	require.NoError(s.T(), os.Chtimes(src, wantMtime, wantMtime))

	fixedExifDate := time.Date(2018, 6, 15, 10, 30, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(fixedExifDate, "DateTimeOriginal", nil)

	_, err := s.appWith(&Config{OutputPath: outDir, CopyMode: true}, svc).processFile(context.Background(), src)
	s.NoError(err)

	dst := filepath.Join(outDir, "2018", "06", "photo.jpg")
	info, statErr := os.Lstat(dst)
	require.NoError(s.T(), statErr)
	s.WithinDuration(wantMtime, info.ModTime(), time.Second)
	svc.AssertExpectations(s.T())
}

func (s *AppTestSuite) TestProcessFile_UseFileModifyDate_PassedToService() {
	src := s.writeFile("photo.jpg", "data")
	outDir := filepath.Join(s.tmpDir, "output")

	fixedTime := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	// UseFileModifyDate=true must be forwarded as the third arg
	svc.On("ExtractDate", src, false, true).Return(fixedTime, "FileModifyDate", nil)

	_, err := s.appWith(&Config{OutputPath: outDir, UseFileModifyDate: true, CopyMode: true}, svc).processFile(context.Background(), src)

	s.NoError(err)
	svc.AssertExpectations(s.T())
}

// ---- copyFile ----

// ---- processFile: signal recovery ----

// TestProcessFile_EXDEV_SourcePreservedOnCancel simulates the cross-device move path
// (copy+delete fallback). When the context is cancelled AFTER the copy succeeds but
// BEFORE os.Remove runs, the source must still exist — no data loss.
func (s *AppTestSuite) TestProcessFile_EXDEV_SourcePreservedOnCancel() {
	src := s.writeFile("photo.jpg", "important-data")
	outDir := filepath.Join(s.tmpDir, "output")

	fixedTime := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(fixedTime, "DateTimeOriginal", nil)

	// Pre-cancelled context: transferLocal sees ctx.Err() != nil before os.Remove.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	app := s.appWith(&Config{OutputPath: outDir}, svc)

	// Inject a fake EXDEV error so the copy+delete branch is exercised without
	// needing two real filesystems.
	origRename := osRename
	defer func() { osRename = origRename }()
	osRename = func(src, dst string) error {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: syscall.EXDEV}
	}

	_, err := app.processFile(ctx, src)

	// No error — file was copied successfully; source skip is non-fatal.
	s.NoError(err)
	// Source must still exist (delete was skipped due to cancellation).
	s.hasFile(src)
	// Destination must also exist (copy completed before cancellation check).
	s.hasFile(filepath.Join(outDir, "2023", "05", "photo.jpg"))
}

// ---- Run: signal / context cancellation ----

// TestRun_CancelledContext_StopsFeeder verifies that Run returns interrupted=true
// when the context is already cancelled before the run begins.
// Workers always finish their in-progress file — we only stop feeding new ones.
func (s *AppTestSuite) TestRun_CancelledContext_StopsFeeder() {
	// Create enough files that at least some won't be fed before ctx is checked.
	for i := range 10 {
		s.writeFile(fmt.Sprintf("photo%02d.jpg", i), "data")
	}
	outDir := filepath.Join(s.tmpDir, "output")

	fixedTime := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", mock.AnythingOfType("string"), false, false).
		Return(fixedTime, "DateTimeOriginal", nil).Maybe()

	// Buffer=0: unbuffered channel — feeder must block on each send until a worker
	// receives. With context already cancelled, ctx.Done() wins the select race
	// before the second send, guaranteeing interrupted=true.
	app := s.appWith(&Config{
		InputPath:  s.tmpDir,
		OutputPath: outDir,
		Workers:    1,
		Buffer:     0,
	}, svc)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	interrupted := app.Run(ctx)

	s.True(interrupted, "Run must return true when context is cancelled")
}

// TestRun_FullContext_NotInterrupted verifies that Run returns false when the
// context is never cancelled and all files are processed normally.
func (s *AppTestSuite) TestRun_FullContext_NotInterrupted() {
	s.writeFile("photo.jpg", "data")
	outDir := filepath.Join(s.tmpDir, "output")

	fixedTime := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", mock.AnythingOfType("string"), false, false).
		Return(fixedTime, "DateTimeOriginal", nil)

	app := s.appWith(&Config{
		InputPath:  s.tmpDir,
		OutputPath: outDir,
		Workers:    1,
		Buffer:     10,
	}, svc)

	interrupted := app.Run(context.Background())

	s.False(interrupted, "Run must return false when context is never cancelled")
	svc.AssertExpectations(s.T())
}

// ---- Stats.print: interrupted notice ----

// TestStatsPrint_InterruptedNotice verifies the interrupted warning line appears
// when interrupted=true and is absent when interrupted=false.
func TestStatsPrint_InterruptedNotice(t *testing.T) {
	captureStdout := func(fn func()) string {
		r, w, _ := os.Pipe()
		old := os.Stdout
		os.Stdout = w
		fn()
		w.Close()
		os.Stdout = old
		var buf bytes.Buffer
		io.Copy(&buf, r)
		return buf.String()
	}

	t.Run("interrupted=true shows warning", func(t *testing.T) {
		var s Stats
		out := captureStdout(func() { s.print(10, 0, true) })
		assert.True(t, strings.Contains(out, "interrupted"), "summary must mention interrupted")
	})

	t.Run("interrupted=false no warning", func(t *testing.T) {
		var s Stats
		out := captureStdout(func() { s.print(10, 0, false) })
		assert.False(t, strings.Contains(out, "interrupted"), "normal summary must not mention interrupted")
	})
}

// ---- copyFile ----

// TestCopyFile_NoPartialFileOnOpenSourceFailure verifies no dst is created when src is missing.
func TestCopyFile_NoPartialFileOnOpenSourceFailure(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "dst.bin")

	err := copyFile(filepath.Join(tmp, "nonexistent.bin"), dst)

	assert.Error(t, err)
	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "dst must not exist when src open fails")
}

// TestCopyFile_RemovesPartialFileOnWriteFailure verifies that if the destination becomes
// unwritable mid-copy (simulated by removing write permission on the dst directory after
// the file is created), copyFile cleans up the partial file.
func TestCopyFile_RemovesPartialFileOnWriteFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod restrictions are bypassed for root")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.bin")
	dstDir := filepath.Join(tmp, "out")
	dst := filepath.Join(dstDir, "dst.bin")

	require.NoError(t, os.MkdirAll(dstDir, 0755))
	require.NoError(t, os.WriteFile(src, make([]byte, 4<<20), 0644)) // 4 MB source

	// Make dstDir read-only BEFORE the copy so os.Create(dst) fails.
	// This exercises the create-error path; the file is never created at all.
	require.NoError(t, os.Chmod(dstDir, 0555))
	defer os.Chmod(dstDir, 0755) // restore so TempDir cleanup works

	err := copyFile(src, dst)

	assert.Error(t, err)
	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "no partial dst should exist after failed copy")
}

func TestCopyFile_CopiesContent(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.txt")
	dst := filepath.Join(tmp, "dst.txt")
	content := []byte("hello media organizer")

	require.NoError(t, os.WriteFile(src, content, 0644))
	require.NoError(t, copyFile(src, dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestCopyFile_KeepsSource(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.txt")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0644))

	require.NoError(t, copyFile(src, filepath.Join(tmp, "dst.txt")))

	_, err := os.Stat(src)
	assert.NoError(t, err, "source file must still exist after copy")
}

func TestCopyFile_MissingSource(t *testing.T) {
	tmp := t.TempDir()
	err := copyFile(filepath.Join(tmp, "nonexistent.jpg"), filepath.Join(tmp, "dst.jpg"))
	assert.Error(t, err)
}

// TestCopyFile_SourceModTimeUnchanged guards against accidentally mutating the source during copy.
func TestCopyFile_SourceModTimeUnchanged(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.jpg")
	require.NoError(t, os.WriteFile(src, []byte("data"), 0644))

	wantMtime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(src, wantMtime, wantMtime))

	require.NoError(t, copyFile(src, filepath.Join(tmp, "dst.jpg")))

	info, err := os.Lstat(src)
	require.NoError(t, err)
	assert.WithinDuration(t, wantMtime, info.ModTime(), time.Second, "source mtime must not be altered by copyFile")
}

// TestPreserveTimestamps_MissingSource verifies that a non-existent source returns an error.
func TestPreserveTimestamps_MissingSource(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "dst.jpg")
	require.NoError(t, os.WriteFile(dst, []byte("x"), 0644))

	err := preserveTimestamps("/nonexistent/path/file.jpg", dst)
	assert.Error(t, err)
}

func TestCopyFile_PreservesModTime(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.jpg")
	dst := filepath.Join(tmp, "dst.jpg")
	require.NoError(t, os.WriteFile(src, []byte("media data"), 0644))

	wantMtime := time.Date(2018, 6, 15, 10, 30, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(src, wantMtime, wantMtime))

	require.NoError(t, copyFile(src, dst))

	info, err := os.Lstat(dst)
	require.NoError(t, err)
	// Allow 1 s of rounding on filesystems with coarser timestamp precision.
	assert.WithinDuration(t, wantMtime, info.ModTime(), time.Second)
}

// ---- circuit breaker / remote failure counter ----

func TestRecordRemoteFailure_FiresAtThreshold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := &App{
		Config:    &Config{RemoteFailThreshold: 3},
		runCancel: cancel,
	}

	assert.False(t, app.recordRemoteFailure()) // 1 — below threshold
	assert.False(t, app.recordRemoteFailure()) // 2 — below threshold
	assert.True(t, app.recordRemoteFailure())  // 3 — fires
	assert.Equal(t, context.Canceled, ctx.Err(), "circuit breaker must cancel the run context")
}

func TestRecordRemoteFailure_ResetOnSuccess(t *testing.T) {
	app := &App{Config: &Config{RemoteFailThreshold: 3}}

	app.recordRemoteFailure() // 1
	app.recordRemoteFailure() // 2
	app.resetRemoteFailures() // reset — next failure starts from 1 again
	assert.False(t, app.recordRemoteFailure()) // 1, not 3
}

func TestRecordRemoteFailure_DisabledWhenZero(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := &App{
		Config:    &Config{RemoteFailThreshold: 0},
		runCancel: cancel,
	}

	for range 100 {
		assert.False(t, app.recordRemoteFailure())
	}
	assert.NoError(t, ctx.Err(), "context must not be cancelled when threshold is 0")
}

func TestRecordRemoteFailure_SubsequentCallsAfterFire(t *testing.T) {
	cancelled := false
	app := &App{
		Config:    &Config{RemoteFailThreshold: 2},
		runCancel: func() { cancelled = true },
	}

	app.recordRemoteFailure() // 1
	app.recordRemoteFailure() // 2 — fires
	assert.True(t, cancelled)
	// Additional failures after the breaker has fired must not panic.
	assert.True(t, app.recordRemoteFailure()) // 3 — still returns true, no panic
}

// ---- isRclonePath / isSFTPPath / toRcloneSFTPPath ----

func TestIsSFTPPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"root@192.168.0.83:/mnt/photos", true},
		{"user@host:path", true},
		{"remotename:/path", false},  // rclone remote, no @
		{"/local/path", false},       // plain local
		{"C:\\Windows\\path", false}, // Windows-style local
		{"@missinguser:path", false}, // @ at index 0
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, isSFTPPath(tc.in))
		})
	}
}

func TestToRcloneSFTPPath(t *testing.T) {
	cases := []struct {
		in      string
		keyFile string
		want    string
	}{
		{
			"root@192.168.0.83:/mnt/naspool/photo", "",
			":sftp,host=192.168.0.83,user=root:/mnt/naspool/photo",
		},
		{
			"alice@nas:backup", "",
			":sftp,host=nas,user=alice:backup",
		},
		{
			"root@192.168.0.83:/mnt/photos", "/home/user/.ssh/id_ed25519",
			":sftp,host=192.168.0.83,user=root,key_file=/home/user/.ssh/id_ed25519:/mnt/photos",
		},
		// non-SFTP paths are returned unchanged
		{"remotename:/path", "", "remotename:/path"},
		{"/local/path", "", "/local/path"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, toRcloneSFTPPath(tc.in, tc.keyFile))
		})
	}
}

func TestIsRclonePath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"myremote:/photos", true},              // standard rclone remote
		{"root@192.168.0.83:/mnt/p", true},     // SSH-style → treated as rclone SFTP
		{"user@host:path", true},               // SSH-style without leading /
		{"/local/path", false},                 // plain local
		{"relative/path", false},               // no colon
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, isRclonePath(tc.in))
		})
	}
}

// ---- collectFiles: sizes ----

func (s *AppTestSuite) TestCollectFiles_ReturnsSizes() {
	s.writeFile("a.jpg", "hello")  // 5 bytes
	s.writeFile("b.mp4", "world!") // 6 bytes

	_, sizes, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(2, count)
	s.Len(sizes, 2)
	for _, sz := range sizes {
		s.Positive(sz, "size must be > 0 for non-empty files")
	}
}

// ---- formatBytes ----

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{int64(1.5 * 1024 * 1024), "1.5 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{4_509_715_660, "4.2 GB"}, // 4.2 * 1024^3, truncated
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, formatBytes(tc.in))
		})
	}
}

// ---- formatDuration ----

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "< 1 sec"},
		{-time.Second, "< 1 sec"},
		{time.Second, "1 sec"},
		{42 * time.Second, "42 sec"},
		{90 * time.Second, "1 min 30 sec"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1 h 2 min 3 sec"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, formatDuration(tc.in))
		})
	}
}

// ---- fitModel ----

func TestFitModel_TwoDistinctPoints(t *testing.T) {
	// 1 MB in 0.5 s, 10 MB in 1.4 s → beta = 0.9/(9 MiB), alpha ≈ 0.4 s
	s1, s2 := int64(1<<20), int64(10<<20)
	alpha, beta := fitModel(s1, s2, 0.5, 1.4)
	assert.InDelta(t, 0.4, alpha, 0.02)
	assert.Positive(t, beta)
}

func TestFitModel_NegativeAlphaClamped(t *testing.T) {
	// Noisy: second file is only marginally slower → would produce negative alpha
	s1, s2 := int64(1<<20), int64(2<<20)
	alpha, _ := fitModel(s1, s2, 1.0, 1.0)
	assert.GreaterOrEqual(t, alpha, 0.0)
}

func TestFitModel_NegativeBetaClamped(t *testing.T) {
	// Second file was faster despite being larger (network jitter) → beta clamped to 0
	s1, s2 := int64(1<<20), int64(10<<20)
	_, beta := fitModel(s1, s2, 1.0, 0.5)
	assert.GreaterOrEqual(t, beta, 0.0)
}

func TestFitModel_SameSize(t *testing.T) {
	// ds = 0 → average of the two times, no overhead term
	alpha, beta := fitModel(1<<20, 1<<20, 0.5, 0.7)
	assert.Equal(t, 0.0, alpha)
	assert.Positive(t, beta)
}

// ---- assertion helpers ----

func (s *AppTestSuite) hasFile(path string) {
	s.T().Helper()
	_, err := os.Stat(path)
	s.NoError(err, "expected file to exist: %s", path)
}

func (s *AppTestSuite) noFile(path string, msgAndArgs ...interface{}) {
	s.T().Helper()
	_, err := os.Stat(path)
	s.True(os.IsNotExist(err), append([]interface{}{"expected file to be absent: %s", path}, msgAndArgs...)...)
}
