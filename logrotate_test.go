package logrotate

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v2"
)

// !!!NOTE!!!
//
// Running these tests in parallel will almost certainly cause sporadic (or even
// regular) failures, because they're all messing with the same global variable
// that controls the logic's mocked time.Now.  So... don't do that.

// Since some tests uses the time to determine filenames etc, we need to
// control the wall clock as much as possible, which means having a wall clock
// that doesn't change unless we want it to.

const (
	backupTimeFormat = "2006-01-02T15-04-05.000"
)

var fakeCurrentTime = time.Now()

func fakeTime() time.Time {
	return fakeCurrentTime
}

func TestNewFile(t *testing.T) {
	currentTime = fakeTime

	dir := makeTempDir("TestNewFile", t)
	defer os.RemoveAll(dir)
	l := &Logger{
		Filename: logFile(dir),
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)
	existsWithContent(logFile(dir), b, t)
	fileCount(dir, 1, t)
}

func TestOpenExisting(t *testing.T) {
	currentTime = fakeTime
	dir := makeTempDir("TestOpenExisting", t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	data := []byte("foo!")
	err := os.WriteFile(filename, data, 0644)
	isNil(err, t)
	existsWithContent(filename, data, t)

	l := &Logger{
		Filename: filename,
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	// make sure the file got appended
	existsWithContent(filename, append(data, b...), t)

	// make sure no other files were created
	fileCount(dir, 1, t)
}

func TestOpenExistingOrNew(t *testing.T) {
	dir := makeTempDir("TestOpenExistingOrNew", t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		Filename: filename,
		MaxBytes: 100,
	}

	// File doesn't exist
	err := l.openExistingOrNew()
	isNil(err, t)
	assert(l.file != nil, t, "Expected file to be opened")
	equals(int64(0), l.size, t)

	content := []byte("Hello, World!")
	_, err = l.file.Write(content)
	isNil(err, t)
	l.size = int64(len(content))

	// Close
	err = l.file.Close()
	isNil(err, t)
	l.file = nil

	// File exists and is not over MaxBytes
	err = l.openExistingOrNew()
	isNil(err, t)
	assert(l.file != nil, t, "Expected file to be opened")
	equals(int64(len(content)), l.size, t)

	// Close
	err = l.file.Close()
	isNil(err, t)
	l.file = nil
}

func TestWriteTooLong(t *testing.T) {
	currentTime = fakeTime
	dir := makeTempDir("TestWriteTooLong", t)
	defer os.RemoveAll(dir)
	l := &Logger{
		Filename: logFile(dir),
		MaxBytes: 1,
	}
	defer l.Close()
	b := []byte("booooooooooooooo!")
	n, err := l.Write(b)
	notNil(err, t)
	equals(0, n, t)
	equals(err.Error(),
		fmt.Sprintf("write length %d exceeds maximum file size %d", len(b), l.MaxBytes), t)
	_, err = os.Stat(logFile(dir))
	assert(os.IsNotExist(err), t, "File exists, but should not have been created")
}

func TestMakeLogDir(t *testing.T) {
	currentTime = fakeTime
	dir := "TestMakeLogDir"
	dir = filepath.Join(os.TempDir(), dir)
	defer os.RemoveAll(dir)
	filename := logFile(dir)
	l := &Logger{
		Filename: filename,
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)
	existsWithContent(logFile(dir), b, t)
	fileCount(dir, 1, t)
}

func TestDefaultFilename(t *testing.T) {
	currentTime = fakeTime
	dir := os.TempDir()
	filename := filepath.Join(dir, filepath.Base(os.Args[0])+".log")
	t.Log(filename)
	defer os.Remove(filename)
	l := &Logger{}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)

	isNil(err, t)
	equals(len(b), n, t)
	existsWithContent(filename, b, t)
}

func TestAutoRotateBackupWithTime(t *testing.T) {
	currentTime = fakeTime

	dir := makeTempDir("TestAutoRotateBackupWithTime", t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		Filename:           filename,
		FilenameTimeFormat: backupTimeFormat,
		MaxBytes:           10,
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	fileCount(dir, 1, t)

	newFakeTime()

	b2 := []byte("foooooo!")
	n, err = l.Write(b2)
	isNil(err, t)
	equals(len(b2), n, t)

	// the old logfile should be moved aside and the main logfile should have
	// only the last write in it.
	existsWithContent(filename, b2, t)

	// the backup file will use the current fake time and have the old contents.
	existsWithContent(backupFileWithTime(dir, backupTimeFormat), b, t)

	fileCount(dir, 2, t)
}

func TestAutoRotateBackupWithOrder(t *testing.T) {
	currentTime = time.Now
	dir := makeTempDir("TestAutoRotateBackupWithOrder", t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		Filename: filename,
		MaxBytes: 10,
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	fileCount(dir, 1, t)

	b2 := []byte("foooooo!")
	n, err = l.Write(b2)
	isNil(err, t)
	equals(len(b2), n, t)

	// the old logfile should be moved aside and the main logfile should have
	// only the last write in it.
	existsWithContent(filename, b2, t)

	// the backup file will use the current fake time and have the old contents.
	existsWithContent(backupFileWithOrder(dir, 1), b, t)

	fileCount(dir, 2, t)
}

func TestFirstWriteRotateBackupWithTime(t *testing.T) {
	currentTime = fakeTime

	dir := makeTempDir("TestFirstWriteRotateBackupWithTime", t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		Filename:           filename,
		FilenameTimeFormat: backupTimeFormat,
		MaxBytes:           10,
	}
	defer l.Close()

	start := []byte("boooooo!")
	err := os.WriteFile(filename, start, 0600)
	isNil(err, t)

	newFakeTime()

	// this would make us rotate
	b := []byte("fooo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	existsWithContent(backupFileWithTime(dir, backupTimeFormat), start, t)

	fileCount(dir, 2, t)
}

func TestFirstWriteRotateBackupWithOrder(t *testing.T) {
	currentTime = time.Now
	dir := makeTempDir("TestFirstWriteRotateBackupWithOrder", t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		Filename: filename,
		MaxBytes: 10,
	}
	defer l.Close()

	start := []byte("boooooo!")
	err := os.WriteFile(filename, start, 0600)
	isNil(err, t)

	// this would make us rotate
	b := []byte("fooo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	existsWithContent(backupFileWithOrder(dir, 1), start, t)

	fileCount(dir, 2, t)
}

func TestMaxBackupsWithTime(t *testing.T) {
	currentTime = fakeTime
	dir := makeTempDir("TestMaxBackupsWithTime", t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		Filename:           filename,
		FilenameTimeFormat: backupTimeFormat,
		MaxBytes:           10,
		MaxBackups:         1,
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	fileCount(dir, 1, t)

	newFakeTime()

	// this will put us over the max
	b2 := []byte("foooooo!")
	n, err = l.Write(b2)
	isNil(err, t)
	equals(len(b2), n, t)

	// this will use the new fake time
	secondFilename := backupFileWithTime(dir, backupTimeFormat)
	existsWithContent(secondFilename, b, t)

	// make sure the old file still exists with the same content.
	existsWithContent(filename, b2, t)

	fileCount(dir, 2, t)

	newFakeTime()

	// this will make us rotate again
	b3 := []byte("baaaaaar!")
	n, err = l.Write(b3)
	isNil(err, t)
	equals(len(b3), n, t)

	// this will use the new fake time
	thirdFilename := backupFileWithTime(dir, backupTimeFormat)
	existsWithContent(thirdFilename, b2, t)

	existsWithContent(filename, b3, t)

	// should only have two files in the dir still
	fileCount(dir, 2, t)

	// second file name should still exist
	existsWithContent(thirdFilename, b2, t)

	// should have deleted the first backup
	notExist(secondFilename, t)

	// now test that we don't delete directories or non-logfile files

	newFakeTime()

	// create a file that is close to but different from the logfile name.
	// It shouldn't get caught by our deletion filters.
	notlogfile := logFile(dir) + ".foo"
	err = os.WriteFile(notlogfile, []byte("data"), 0644)
	isNil(err, t)

	// Make a directory that exactly matches our log file filters... it still
	// shouldn't get caught by the deletion filter since it's a directory.
	notlogfiledir := backupFileWithTime(dir, backupTimeFormat)
	err = os.Mkdir(notlogfiledir, 0700)
	isNil(err, t)

	newFakeTime()

	// this will use the new fake time
	fourthFilename := backupFileWithTime(dir, backupTimeFormat)

	// Create a log file that is/was being compressed - this should
	// not be counted since both the compressed and the uncompressed
	// log files still exist.
	compLogFile := fourthFilename + compressSuffix
	err = os.WriteFile(compLogFile, []byte("compress"), 0644)
	isNil(err, t)

	// this will make us rotate again
	b4 := []byte("baaaaaaz!")
	n, err = l.Write(b4)
	isNil(err, t)
	equals(len(b4), n, t)

	existsWithContent(fourthFilename, b3, t)
	existsWithContent(fourthFilename+compressSuffix, []byte("compress"), t)

	// We should have four things in the directory now - the 2 log files, the
	// not log file, and the directory
	fileCount(dir, 5, t)

	// third file name should still exist
	existsWithContent(filename, b4, t)

	existsWithContent(fourthFilename, b3, t)

	// should have deleted the first filename
	notExist(thirdFilename, t)

	// the not-a-logfile should still exist
	exists(notlogfile, t)

	// the directory
	exists(notlogfiledir, t)
}

func TestMaxBackupsWithOrder(t *testing.T) {
	// TODO fix this test when https://github.com/fahedouch/go-logrotate/issues/11 is resolved
	t.Skip()
	currentTime = time.Now
	dir := makeTempDir("TestMaxBackupsWithOrder", t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		Filename:   filename,
		MaxBytes:   10,
		MaxBackups: 1,
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	fileCount(dir, 1, t)

	// this will put us over the max
	b2 := []byte("foooooo!")
	n, err = l.Write(b2)
	isNil(err, t)
	equals(len(b2), n, t)

	secondFilename := backupFileWithOrder(dir, 1)
	existsWithContent(secondFilename, b, t)

	// make sure the old file still exists with the same content.
	existsWithContent(filename, b2, t)

	fileCount(dir, 2, t)

	// this will make us rotate again
	b3 := []byte("baaaaaar!")
	n, err = l.Write(b3)
	isNil(err, t)
	equals(len(b3), n, t)

	thirdFilename := backupFileWithOrder(dir, 2)
	existsWithContent(thirdFilename, b2, t)
	existsWithContent(filename, b3, t)

	// should only have two files in the dir still
	fileCount(dir, 2, t)

	// second file name should still exist
	existsWithContent(thirdFilename, b2, t)

	// should have deleted the first backup
	notExist(secondFilename, t)

	// now test that we don't delete directories or non-logfile files

	// create a file that is close to but different from the logfile name.
	// It shouldn't get caught by our deletion filters.
	notlogfile := logFile(dir) + ".4.foo"
	err = os.WriteFile(notlogfile, []byte("data"), 0644)
	isNil(err, t)

	// Make a directory that exactly matches our log file filters... it still
	// shouldn't get caught by the deletion filter since it's a directory.
	notlogfiledir := backupFileWithOrder(dir, 4)
	err = os.Mkdir(notlogfiledir, 0700)
	isNil(err, t)

	// this will use the new fake time
	fourthFilename := backupFileWithOrder(dir, 3)

	// Create a log file that is/was being compressed - this should
	// not be counted since both the compressed and the uncompressed
	// log files still exist.
	compLogFile := fourthFilename + compressSuffix
	err = os.WriteFile(compLogFile, []byte("compress"), 0644)
	isNil(err, t)

	// this will make us rotate again
	b4 := []byte("baaaaaaz!")
	n, err = l.Write(b4)
	isNil(err, t)
	equals(len(b4), n, t)

	existsWithContent(fourthFilename, b3, t)
	existsWithContent(fourthFilename+compressSuffix, []byte("compress"), t)

	// We should have four things in the directory now - the 2 log files, the
	// not log file, and the directory
	fileCount(dir, 5, t)

	// third file name should still exist
	existsWithContent(filename, b4, t)

	existsWithContent(fourthFilename, b3, t)

	// should have deleted the first filename
	notExist(thirdFilename, t)

	// the not-a-logfile should still exist
	exists(notlogfile, t)

	// the directory
	exists(notlogfiledir, t)
}

func TestCleanupExistingBackupsWithTime(t *testing.T) {
	// test that if we start with more backup files than we're supposed to have
	// in total, that extra ones get cleaned up when we rotate.
	currentTime = fakeTime

	dir := makeTempDir("TestCleanupExistingBackupsWithTime", t)
	defer os.RemoveAll(dir)

	// make 3 backup files

	data := []byte("data")
	backup := backupFileWithTime(dir, backupTimeFormat)
	err := os.WriteFile(backup, data, 0644)
	isNil(err, t)

	newFakeTime()

	backup = backupFileWithTime(dir, backupTimeFormat)
	err = os.WriteFile(backup+compressSuffix, data, 0644)
	isNil(err, t)

	newFakeTime()

	backup = backupFileWithTime(dir, backupTimeFormat)
	err = os.WriteFile(backup, data, 0644)
	isNil(err, t)

	// now create a primary log file with some data
	filename := logFile(dir)
	err = os.WriteFile(filename, data, 0644)
	isNil(err, t)

	l := &Logger{
		Filename:           filename,
		FilenameTimeFormat: backupTimeFormat,
		MaxBytes:           10,
		MaxBackups:         1,
	}
	defer l.Close()

	newFakeTime()

	b2 := []byte("foooooo!")
	n, err := l.Write(b2)
	isNil(err, t)
	equals(len(b2), n, t)

	// now we should only have 2 files left - the primary and one backup
	fileCount(dir, 2, t)
}

func TestCleanupExistingBackupsWithOrder(t *testing.T) {
	// test that if we start with more backup files than we're supposed to have
	// in total, that extra ones get cleaned up when we rotate.
	currentTime = time.Now

	dir := makeTempDir("TestCleanupExistingBackupsWithOrder", t)
	defer os.RemoveAll(dir)

	// make 3 backup files

	data := []byte("data")
	backup := backupFileWithOrder(dir, 1)
	err := os.WriteFile(backup, data, 0644)
	isNil(err, t)

	backup = backupFileWithOrder(dir, 2)
	err = os.WriteFile(backup+compressSuffix, data, 0644)
	isNil(err, t)

	backup = backupFileWithOrder(dir, 3)
	err = os.WriteFile(backup, data, 0644)
	isNil(err, t)

	// now create a primary log file with some data
	filename := logFile(dir)
	err = os.WriteFile(filename, data, 0644)
	isNil(err, t)

	l := &Logger{
		Filename:   filename,
		MaxBytes:   10,
		MaxBackups: 1,
	}
	defer l.Close()

	b2 := []byte("foooooo!")
	n, err := l.Write(b2)
	isNil(err, t)
	equals(len(b2), n, t)

	// now we should only have 2 files left - the primary and one backup
	fileCount(dir, 2, t)
}

func TestMaxAgeOfBackupsWithTime(t *testing.T) {
	currentTime = fakeTime

	dir := makeTempDir(identifier(t), t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		Filename:           filename,
		FilenameTimeFormat: backupTimeFormat,
		MaxBytes:           10,
		MaxAge:             1,
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	fileCount(dir, 1, t)

	// two days later
	newFakeTime()

	b2 := []byte("foooooo!")
	n, err = l.Write(b2)
	isNil(err, t)
	equals(len(b2), n, t)
	existsWithContent(backupFileWithTime(dir, backupTimeFormat), b, t)

	// We should still have 2 log files, since the most recent backup was just
	// created.
	fileCount(dir, 2, t)

	existsWithContent(filename, b2, t)

	// we should have deleted the old file due to being too old
	existsWithContent(backupFileWithTime(dir, backupTimeFormat), b, t)

	// two days later
	newFakeTime()

	b3 := []byte("baaaaar!")
	n, err = l.Write(b3)
	isNil(err, t)
	equals(len(b3), n, t)
	existsWithContent(backupFileWithTime(dir, backupTimeFormat), b2, t)

	// We should have 2 log files - the main log file, and the most recent
	// backup.  The earlier backup is past the cutoff and should be gone.
	fileCount(dir, 2, t)

	existsWithContent(filename, b3, t)

	// we should have deleted the old file due to being too old
	existsWithContent(backupFileWithTime(dir, backupTimeFormat), b2, t)
}

// TODO TestMaxAgeOfBackupsWithOrder

func TestOldLogFiles(t *testing.T) {
	currentTime = fakeTime

	dir := makeTempDir(identifier(t), t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	data := []byte("data")
	err := os.WriteFile(filename, data, 07)
	isNil(err, t)

	backup := backupFileWithOrder(dir, 1)
	err = os.WriteFile(backup, data, 07)
	isNil(err, t)

	time.Sleep(1 * time.Second)
	backup2 := backupFileWithOrder(dir, 2)
	err = os.WriteFile(backup2, data, 07)
	isNil(err, t)

	l := &Logger{Filename: filename}
	files, err := l.oldLogFiles()
	isNil(err, t)
	equals(2, len(files), t)

	// should be sorted by newest file first
	assert(files[1].timestamp.Before(files[0].timestamp), t, "log files should be sorted by newest file first")
}

func TestTimeFromFileName(t *testing.T) {
	l := &Logger{Filename: "/var/log/myfoo/foo.log", FilenameTimeFormat: backupTimeFormat}
	prefix, ext := l.prefixAndExt()

	tests := []struct {
		filename string
		want     time.Time
		wantErr  bool
	}{
		{"foo-2014-05-04T14-44-33.555.log", time.Date(2014, 5, 4, 14, 44, 33, 555000000, time.UTC), false},
		{"foo-2014-05-04T14-44-33.555", time.Time{}, true},
		{"2014-05-04T14-44-33.555.log", time.Time{}, true},
		{"foo.log", time.Time{}, true},
	}

	for _, test := range tests {
		got, err := l.timeFromName(test.filename, prefix, ext)
		equals(got, test.want, t)
		equals(err != nil, test.wantErr, t)
	}
}

func TestOrderFromFileName(t *testing.T) {
	l := &Logger{Filename: "/var/log/myfoo/foo.log"}
	prefix, ext := l.prefixAndExt()
	tests := []struct {
		filename string
		want     int
		wantErr  bool
	}{
		{"foo.log.1", 1, false},
		{"foo", 0, true},
		{".log", 0, true},
		{"foo.xls", 0, true},
	}

	for _, test := range tests {
		got, err := l.orderFromName(test.filename, prefix, ext)
		equals(got, test.want, t)
		equals(err != nil, test.wantErr, t)
	}
}

func TestLocalTime(t *testing.T) {
	currentTime = fakeTime

	dir := makeTempDir(identifier(t), t)
	defer os.RemoveAll(dir)

	l := &Logger{
		Filename:           logFile(dir),
		FilenameTimeFormat: backupTimeFormat,
		MaxBytes:           10,
		LocalTime:          true,
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	b2 := []byte("fooooooo!")
	n2, err := l.Write(b2)
	isNil(err, t)
	equals(len(b2), n2, t)

	existsWithContent(logFile(dir), b2, t)
	existsWithContent(backupFileLocal(dir, backupTimeFormat), b, t)
}

func TestRotateBackupsWithTime(t *testing.T) {
	currentTime = fakeTime
	dir := makeTempDir(identifier(t), t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)

	l := &Logger{
		Filename:           filename,
		FilenameTimeFormat: backupTimeFormat,
		MaxBackups:         1,
		MaxBytes:           100, // bytes
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	fileCount(dir, 1, t)

	newFakeTime()

	err = l.Rotate()
	isNil(err, t)

	filename2 := backupFileWithTime(dir, backupTimeFormat)
	existsWithContent(filename2, b, t)
	existsWithContent(filename, []byte{}, t)
	fileCount(dir, 2, t)
	newFakeTime()

	err = l.Rotate()
	isNil(err, t)

	filename3 := backupFileWithTime(dir, backupTimeFormat)
	existsWithContent(filename3, []byte{}, t)
	existsWithContent(filename, []byte{}, t)
	fileCount(dir, 2, t)

	b2 := []byte("foooooo!")
	n, err = l.Write(b2)
	isNil(err, t)
	equals(len(b2), n, t)

	// this will use the new fake time
	existsWithContent(filename, b2, t)
}

func TestRotateBackupsWithOrder(t *testing.T) {
	currentTime = time.Now
	dir := makeTempDir(identifier(t), t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)

	l := &Logger{
		Filename:   filename,
		MaxBackups: 3,
		MaxBytes:   100, // bytes
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	fileCount(dir, 1, t)

	err = l.Rotate()
	isNil(err, t)

	filename1 := backupFileWithOrder(dir, 1)
	existsWithContent(filename1, b, t)
	existsWithContent(filename, []byte{}, t)
	fileCount(dir, 2, t)

	err = l.Rotate()
	isNil(err, t)

	filename2 := backupFileWithOrder(dir, 2)
	existsWithContent(filename1, b, t)
	existsWithContent(filename2, []byte{}, t)
	existsWithContent(filename, []byte{}, t)
	fileCount(dir, 3, t)

	b2 := []byte("foooooo!")
	n, err = l.Write(b2)
	isNil(err, t)
	equals(len(b2), n, t)

	// this will use the new fake time
	existsWithContent(filename, b2, t)
}

func TestCompressBackupsWithTimeOnRotate(t *testing.T) {
	currentTime = fakeTime

	dir := makeTempDir(identifier(t), t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		Compress:           true,
		Filename:           filename,
		FilenameTimeFormat: backupTimeFormat,
		MaxBytes:           10,
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	fileCount(dir, 1, t)

	newFakeTime()

	err = l.Rotate()
	isNil(err, t)

	// the old logfile should be moved aside and the main logfile should have
	// nothing in it.
	existsWithContent(filename, []byte{}, t)

	// a compressed version of the log file should now exist and the original
	// should have been removed.
	bc := new(bytes.Buffer)
	gz := gzip.NewWriter(bc)
	_, err = gz.Write(b)
	isNil(err, t)
	err = gz.Close()
	isNil(err, t)
	existsWithContent(backupFileWithTime(dir, backupTimeFormat)+compressSuffix, bc.Bytes(), t)
	notExist(backupFileWithTime(dir, backupTimeFormat), t)

	fileCount(dir, 2, t)
}

func TestCompressBackupsWithOrderOnRotate(t *testing.T) {
	currentTime = time.Now
	dir := makeTempDir(identifier(t), t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		Compress: true,
		Filename: filename,
		MaxBytes: 10,
	}
	defer l.Close()
	b := []byte("boo!")
	n, err := l.Write(b)
	isNil(err, t)
	equals(len(b), n, t)

	existsWithContent(filename, b, t)
	fileCount(dir, 1, t)

	err = l.Rotate()
	isNil(err, t)

	// the old logfile should be moved aside and the main logfile should have
	// nothing in it.
	existsWithContent(filename, []byte{}, t)

	// a compressed version of the log file should now exist and the original
	// should have been removed.
	bc := new(bytes.Buffer)
	gz := gzip.NewWriter(bc)
	_, err = gz.Write(b)
	isNil(err, t)
	err = gz.Close()
	isNil(err, t)
	existsWithContent(backupFileWithOrder(dir, 1)+compressSuffix, bc.Bytes(), t)
	notExist(backupFileWithOrder(dir, 1), t)

	fileCount(dir, 2, t)
}

func TestCompressOnResume(t *testing.T) {
	currentTime = fakeTime

	dir := makeTempDir(identifier(t), t)
	defer os.RemoveAll(dir)

	filename := logFile(dir)
	l := &Logger{
		FilenameTimeFormat: backupTimeFormat,
		Compress:           true,
		Filename:           filename,
		MaxBytes:           10,
	}
	defer l.Close()

	// Create a backup file and empty "compressed" file.
	filename2 := backupFileWithTime(dir, backupTimeFormat)
	b := []byte("foo!")
	err := os.WriteFile(filename2, b, 0644)
	isNil(err, t)
	err = os.WriteFile(filename2+compressSuffix, []byte{}, 0644)
	isNil(err, t)

	newFakeTime()

	b2 := []byte("boo!")
	n, err := l.Write(b2)
	isNil(err, t)
	equals(len(b2), n, t)
	existsWithContent(filename, b2, t)

	// we need to wait a little bit since the files get compressed on a different
	// goroutine.
	<-time.After(300 * time.Millisecond)

	// The write should have started the compression - a compressed version of
	// the log file should now exist and the original should have been removed.
	bc := new(bytes.Buffer)
	gz := gzip.NewWriter(bc)
	_, err = gz.Write(b)
	isNil(err, t)
	err = gz.Close()
	isNil(err, t)
	existsWithContent(filename2+compressSuffix, bc.Bytes(), t)
	notExist(filename2, t)

	fileCount(dir, 2, t)
}

func TestJson(t *testing.T) {
	data := []byte(`
{
	"filename": "foo",
	"maxbytes": 5,
	"maxage": 10,
	"maxbackups": 3,
	"localtime": true,
	"compress": true
}`[1:])

	l := Logger{}
	err := json.Unmarshal(data, &l)
	isNil(err, t)
	equals("foo", l.Filename, t)
	equals(int64(5), l.MaxBytes, t)
	equals(10, l.MaxAge, t)
	equals(3, l.MaxBackups, t)
	equals(true, l.LocalTime, t)
	equals(true, l.Compress, t)
}

func TestYaml(t *testing.T) {
	data := []byte(`
filename: foo
maxbytes: 5
maxage: 10
maxbackups: 3
localtime: true
compress: true`[1:])

	l := Logger{}
	err := yaml.Unmarshal(data, &l)
	isNil(err, t)
	equals("foo", l.Filename, t)
	equals(int64(5), l.MaxBytes, t)
	equals(10, l.MaxAge, t)
	equals(3, l.MaxBackups, t)
	equals(true, l.LocalTime, t)
	equals(true, l.Compress, t)
}

func TestToml(t *testing.T) {
	data := `
filename = "foo"
maxbytes = 5
maxage = 10
maxbackups = 3
localtime = true
compress = true`[1:]

	l := Logger{}
	md, err := toml.Decode(data, &l)
	isNil(err, t)
	equals("foo", l.Filename, t)
	equals(int64(5), l.MaxBytes, t)
	equals(10, l.MaxAge, t)
	equals(3, l.MaxBackups, t)
	equals(true, l.LocalTime, t)
	equals(true, l.Compress, t)
	equals(0, len(md.Undecoded()), t)
}

// makeTempDir creates a temporary directory
func makeTempDir(name string, t testing.TB) string {
	dir := filepath.Join(os.TempDir(), name)
	isNilUp(os.Mkdir(dir, 0700), t, 1)
	return dir
}

// existsWithContent checks that the given file exists and has the correct content.
func existsWithContent(path string, content []byte, t testing.TB) {
	info, err := os.Stat(path)
	isNilUp(err, t, 1)
	equalsUp(int64(len(content)), info.Size(), t, 1)

	b, err := os.ReadFile(path)
	isNilUp(err, t, 1)
	equalsUp(content, b, t, 1)
}

// logFile returns the current log file name in the given directory.
func logFile(dir string) string {
	return filepath.Join(dir, "foobar.log")
}

func backupFileWithOrder(dir string, order int) string {
	return filepath.Join(dir, fmt.Sprintf("foobar.log.%d", order))
}

func backupFileWithTime(dir string, timeFormat string) string {
	return filepath.Join(dir, "foobar-"+fakeTime().UTC().Format(timeFormat)+".log")
}

func backupFileLocal(dir string, timeFormat string) string {
	return filepath.Join(dir, "foobar-"+fakeTime().Format(timeFormat)+".log")
}

// logFileLocal returns the log file name in the given directory for the current
// fake time using the local timezone.
/*func logFileLocal(dir string) string {
	return filepath.Join(dir, fakeTime().Format(backupTimeFormat))
}*/

// fileCount checks that the number of files in the directory is exp.
func fileCount(dir string, exp int, t testing.TB) {
	files, err := os.ReadDir(dir)
	isNilUp(err, t, 1)
	// Make sure no other files were created.
	equalsUp(exp, len(files), t, 1)
}

// newFakeTime sets the fake "current time" to two days later.
func newFakeTime() {
	fakeCurrentTime = fakeCurrentTime.Add(time.Hour * 24 * 2)
}

func notExist(path string, t testing.TB) {
	_, err := os.Stat(path)
	assertUp(os.IsNotExist(err), t, 1, "expected to get os.IsNotExist, but instead got %v", err)
}

func exists(path string, t testing.TB) {
	_, err := os.Stat(path)
	assertUp(err == nil, t, 1, "expected file to exist, but got error from os.Stat: %v", err)
}
