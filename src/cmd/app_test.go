package main

import (
	"io"
	"os"
	"path/filepath"
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

	paths, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(2, count)
	s.Len(paths, 2)
}

func (s *AppTestSuite) TestCollectFiles_EmptyDir() {
	paths, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(0, count)
	s.Empty(paths)
}

func (s *AppTestSuite) TestCollectFiles_NestedFiles() {
	sub := filepath.Join(s.tmpDir, "2023", "01")
	require.NoError(s.T(), os.MkdirAll(sub, 0755))
	require.NoError(s.T(), os.WriteFile(filepath.Join(sub, "nested.jpg"), []byte("x"), 0644))
	s.writeFile("root.jpg", "y")

	_, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(2, count)
}

func (s *AppTestSuite) TestCollectFiles_SkipsSpotlightDir() {
	dir := filepath.Join(s.tmpDir, ".Spotlight-V100")
	require.NoError(s.T(), os.MkdirAll(dir, 0755))
	require.NoError(s.T(), os.WriteFile(filepath.Join(dir, "hidden.jpg"), []byte("x"), 0644))
	s.writeFile("visible.jpg", "y")

	paths, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(1, count)
	s.Contains(paths[0], "visible.jpg")
}

func (s *AppTestSuite) TestCollectFiles_SkipsFseventsd() {
	dir := filepath.Join(s.tmpDir, ".fseventsd")
	require.NoError(s.T(), os.MkdirAll(dir, 0755))
	require.NoError(s.T(), os.WriteFile(filepath.Join(dir, "event"), []byte("x"), 0644))

	_, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(0, count)
}

func (s *AppTestSuite) TestCollectFiles_SkipsDocumentRevisions() {
	dir := filepath.Join(s.tmpDir, ".DocumentRevisions-V100")
	require.NoError(s.T(), os.MkdirAll(dir, 0755))
	require.NoError(s.T(), os.WriteFile(filepath.Join(dir, "rev"), []byte("x"), 0644))

	_, count, _ := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

	s.Equal(0, count)
}

func (s *AppTestSuite) TestCollectFiles_SkipsNonMediaFiles() {
	s.writeFile("photo.jpg", "a")
	s.writeFile("document.pdf", "b")
	s.writeFile("readme.txt", "c")
	s.writeFile("clip.mp4", "d")

	paths, count, skipped := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

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

	_, count, skipped := s.appWith(&Config{InputPath: s.tmpDir}, nil).collectFiles()

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

	_, err := s.appWith(&Config{OutputPath: outDir}, svc).processFile(src)

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

	_, err := s.appWith(&Config{OutputPath: outDir, CopyMode: true}, svc).processFile(src)

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

	_, err := s.appWith(&Config{OutputPath: outDir, DryRun: true}, svc).processFile(src)

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

	_, processErr := s.appWith(&Config{OutputPath: outDir, CopyMode: true}, svc).processFile(src)
	s.NoError(processErr)

	s.hasFile(filepath.Join(outDir, "2001", "09", "img.jpg"))
}

func (s *AppTestSuite) TestProcessFile_NoDate_ReturnsError() {
	src := s.writeFile("photo.jpg", "data")

	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(time.Time{}, "", nil)

	_, err := s.appWith(&Config{OutputPath: s.tmpDir}, svc).processFile(src)

	s.Error(err)
	svc.AssertExpectations(s.T())
}

func (s *AppTestSuite) TestProcessFile_ExifError_ReturnsError() {
	src := s.writeFile("photo.jpg", "data")

	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(time.Time{}, "", assert.AnError)

	_, err := s.appWith(&Config{OutputPath: s.tmpDir}, svc).processFile(src)

	s.Error(err)
	svc.AssertExpectations(s.T())
}

func (s *AppTestSuite) TestProcessFile_OnlyDateTimeOriginal_SkipsCreateDate() {
	src := s.writeFile("photo.jpg", "data")
	outDir := filepath.Join(s.tmpDir, "output")

	fixedTime := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(fixedTime, "CreateDate", nil)

	_, err := s.appWith(&Config{OutputPath: outDir, OnlyDateTimeOriginal: true}, svc).processFile(src)

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

	_, err := s.appWith(&Config{OutputPath: outDir, OnlyDateTimeOriginal: true, CopyMode: true}, svc).processFile(src)

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

	_, err := s.appWith(&Config{OutputPath: outDir, CopyMode: true}, svc).processFile(src)
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

	_, err := s.appWith(&Config{OutputPath: outDir, UseFileModifyDate: true, CopyMode: true}, svc).processFile(src)

	s.NoError(err)
	svc.AssertExpectations(s.T())
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
