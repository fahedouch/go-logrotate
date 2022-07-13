// logrotate.go micmics https://github.com/natefinch/lumberjack/blob/v2.0/lumberjack.go

package logrotate

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/djherbis/times"
)

const (
	compressSuffix = ".gz"
	defaultMaxSize = 100
)

// ensure we always implement io.WriteCloser
var _ io.WriteCloser = (*Logger)(nil)

// Logger is an io.WriteCloser that writes to the specified filename.
//
// Logger opens or creates the logfile on first Write.  If the file exists and
// is less than MaxBytes, logrotate will open and append to that file.
// If the file exists and its size is >= MaxBytes, the file is renamed
// by putting an incremental number after the file's extension or at the end
// of the filename if there's no extension. A new log file is then created using
// original filename.
//
// Whenever a write would cause the current log file exceed MaxBytes,
// the current file is closed, renamed, and a new log file created with the
// original name. Thus, the filename you give Logger is always the "current" log
// file.
//
// Backups uses the log file name given to Logger, in the form
// `name.ext.num` or `name-timestamp.ext` where name is the filename without the extension,
// timestamp is the birth time at which the log was rotated formatted with the
// time.Time format of `2006-01-02T15-04-05.000` and the extension is the
// original extension.
// if Logger.FilenameTimeFormat is not empty the backup name format is `name-timestamp.ext`
// if Logger.FilenameTimeFormat is empty the backup name format is `name.ext.num`
// For example, if your Logger.Filename is `/var/log/foo/server.log` and Logger.FilenameTimeFormat
// is not empty, a backup created at 6:30pm on Nov 11 2016 would
// use the filename `/var/log/foo/server-2016-11-04T18-30-00.000.log`
//
// Cleaning Up Old Log Files
//
// Whenever a new logfile gets created, old log files may be deleted.  The most
// recent files according to their birth time will be retained, up to a
// number equal to MaxBackups (or all of them if MaxBackups is 0).  Any files
// birth time older than MaxAge days are deleted, regardless of
// MaxBackups.Note that the file's birth time is the rotation
// time, which may differ from the last time that file was written to.
//
// If MaxBackups and MaxAge are both 0, no old log files will be deleted.
type Logger struct {
	// Filename is the file to write logs to.  Backup log files will be retained
	// in the same directory.  It uses <processname>.log in
	// os.TempDir() if empty.
	Filename string `json:"filename" yaml:"filename"`

	// FilenameTimeFormat determines whether the rotated log file name contains
	// timestamp or not and defines its format. It doesn't contain timestamp if empty.
	// (e.g `2006-01-02T15-04-05.000`)
	FilenameTimeFormat string `json:"filenameTimeFormat" yaml:"filenameTimeFormat"`

	// MaxBytes is the maximum size in bytes of the log file before it gets
	// rotated. It defaults to 104857600 (100 megabytes).
	MaxBytes int64 `json:"maxbytes" yaml:"maxbytes"`

	// MaxAge is the maximum number of days to retain old log files based on the
	// timestamp encoded in their filename.  Note that a day is defined as 24
	// hours and may not exactly correspond to calendar days due to daylight
	// savings, leap seconds, etc. The default is not to remove old log files
	// based on age.
	MaxAge int `json:"maxage" yaml:"maxage"`

	// MaxBackups is the maximum number of old log files to retain.  The default
	// is to retain all old log files (though MaxAge may still cause them to get
	// deleted.)
	MaxBackups int `json:"maxbackups" yaml:"maxbackups"`

	// LocalTime determines if the time used for formatting the timestamps in
	// backup files is the computer's local time.  The default is to use UTC
	// time.
	LocalTime bool `json:"localtime" yaml:"localtime"`

	// Compress determines if the rotated log files should be compressed
	// using gzip. The default is not to perform compression.
	Compress bool `json:"compress" yaml:"compress"`

	size int64
	file *os.File
	mu   sync.Mutex

	millCh    chan bool
	startMill sync.Once
}

var (
	// currentTime exists so it can be mocked out by tests.
	currentTime = time.Now

	// os_Stat exists so it can be mocked out by tests.
	osStat = os.Stat

	// megabyte is the conversion factor between MaxSize and bytes.  It is a
	// variable so tests can mock it out and not need to write megabytes of data
	// to disk.
	megabyte = 1024 * 1024
)

// Write implements io.Writer.  If a write would cause the log file to be larger
// than MaxBytes, the file is closed, renamed and a new log file is created using the original log file name.
// If the length of the write is greater than MaxBytes, an error is returned.
func (l *Logger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	writeLen := int64(len(p))
	if writeLen > l.max(writeLen) {
		return 0, fmt.Errorf(
			"write length %d exceeds maximum file size %d", writeLen, l.max(writeLen),
		)
	}

	if l.file == nil {
		if err = l.openExistingOrNew(len(p)); err != nil {
			return 0, err
		}
	}

	if l.size+writeLen > l.max(writeLen) {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = l.file.Write(p)
	l.size += int64(n)

	return n, err
}

// Close implements io.Closer, and closes the current logfile.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.close()
}

// close closes the file if it is open.
func (l *Logger) close() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Rotate causes Logger to close the existing log file and immediately create a
// new one.  This is a helper function for applications that want to initiate
// rotations outside of the normal rotation rules, such as in response to
// SIGHUP.  After rotating, this initiates compression and removal of old log
// files according to the configuration.
func (l *Logger) Rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rotate()
}

// rotate closes the current file, moves it aside with either a timestamp
// in the name or number at the end of the name, (if it exists),
// opens a new file with the original filename, and then runs post-rotation processing and removal.
func (l *Logger) rotate() error {
	if err := l.close(); err != nil {
		return err
	}
	if err := l.openNew(); err != nil {
		return err
	}
	l.mill()
	return nil
}

// openNew opens a new log file for writing, moving any old log file out of the
// way.  This methods assumes the file has already been closed.
func (l *Logger) openNew() error {
	err := os.MkdirAll(l.dir(), 0755)
	if err != nil {
		return fmt.Errorf("can't make directories for new logfile: %s", err)
	}

	name := l.filename()
	mode := os.FileMode(0600)
	info, err := osStat(name)
	if err == nil {
		// Copy the mode off the old logfile.
		mode = info.Mode()
		// move the existing file
		newname, err := l.backupName(name, l.FilenameTimeFormat, l.LocalTime)
		if err != nil {
			return err
		}
		if err := os.Rename(name, newname); err != nil {
			return fmt.Errorf("can't rename log file: %s", err)
		}

		// Set both access time and modified time of the backup file to the current time
		// We will use the file Mod time to get time informations of backup file with standard name format
		if l.FilenameTimeFormat == "" {
			err := os.Chtimes(newname, currentTime(), currentTime())
			if err != nil {
				return err
			}
		}
		// this is a no-op anywhere but linux
		if err := chown(name, info); err != nil {
			return err
		}
	}

	// we use truncate here because this should only get called when we've moved
	// the file ourselves. if someone else creates the file in the meantime,
	// just wipe out the contents.
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("can't open new logfile: %s", err)
	}

	l.file = f
	l.size = 0
	return nil
}

// backupName creates a new filename
func (l *Logger) backupName(name, nameTimeFormat string, local bool) (string, error) {
	dir := filepath.Dir(name)
	prefix, ext := l.prefixAndExt()
	var filename string
	if nameTimeFormat != "" {
		t := currentTime()
		if !local {
			t = t.UTC()
		}
		timestamp := t.Format(nameTimeFormat)
		filename = fmt.Sprintf("%s%s%s", prefix, timestamp, ext)
	} else {
		oldFiles, err := l.oldLogFiles()
		if err != nil {
			return "", err
		}
		var maxBackupOrder int
		for _, f := range oldFiles {
			if !strings.HasSuffix(f.Name(), compressSuffix) {
				if order, err := l.orderFromName(f.Name(), prefix, ext); err == nil {
					if maxBackupOrder < order {
						maxBackupOrder = order
					}
				}
			}
		}
		filename = fmt.Sprintf("%s%s.%d", prefix, ext, maxBackupOrder+1)
	}

	return filepath.Join(dir, filename), nil
}

// openExistingOrNew opens the logfile if it exists and if the current write
// would not put it over MaxBytes.  If there is no such file or the write would
// put it over the MaxBytes, a new file is created.
func (l *Logger) openExistingOrNew(writeLen int) error {
	l.mill()

	filename := l.filename()
	info, err := osStat(filename)
	if os.IsNotExist(err) {
		return l.openNew()
	}
	if err != nil {
		return fmt.Errorf("error getting log file info: %s", err)
	}

	if info.Size()+int64(writeLen) >= l.max(int64(writeLen)) {
		return l.rotate()
	}

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		// if we fail to open the old log file for some reason, just ignore
		// it and open a new log file.
		return l.openNew()
	}
	l.file = file
	l.size = info.Size()
	return nil
}

// filename generates the name of the logfile.
func (l *Logger) filename() string {
	if l.Filename != "" {
		return l.Filename
	}
	name := filepath.Base(os.Args[0]) + ".log"
	return filepath.Join(os.TempDir(), name)
}

// millRunOnce performs compression and removal of stale log files.
// Log files are compressed if enabled via configuration and old log
// files are removed, keeping at most l.MaxBackups files, as long as
// none of them are older than MaxAge.
func (l *Logger) millRunOnce() error {
	if l.MaxBackups == 0 && l.MaxAge == 0 && !l.Compress {
		return nil
	}

	files, err := l.oldLogFiles()
	if err != nil {
		return err
	}
	var compress, remove []logInfo

	fofo, _ := os.OpenFile("/tmp/solution3.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	for _, f := range files {
		if _, err := fofo.Write([]byte(f.Name() + "\n")); err != nil {
			fofo.Close() // ignore error; Write error takes precedence
		}
	}

	if l.MaxBackups > 0 && l.MaxBackups < len(files) {
		preserved := make(map[string]bool)
		var remaining []logInfo
		fofo, _ := os.OpenFile("/tmp/solution.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		for _, f := range files {
			if _, err := fofo.Write([]byte(f.Name() + "\n")); err != nil {
				fofo.Close() // ignore error; Write error takes precedence
			}
			// Only count the uncompressed log file or the
			// compressed log file, not both.
			fn := strings.TrimSuffix(f.Name(), compressSuffix)
			preserved[fn] = true

			if len(preserved) > l.MaxBackups {
				remove = append(remove, f)
			} else {
				remaining = append(remaining, f)
			}
		}
		files = remaining
	}
	if l.MaxAge > 0 {
		diff := time.Duration(int64(24*time.Hour) * int64(l.MaxAge))
		cutoff := currentTime().Add(-1 * diff)

		var remaining []logInfo
		for _, f := range files {
			if f.timestamp.Before(cutoff) {
				remove = append(remove, f)
			} else {
				remaining = append(remaining, f)
			}
		}
		files = remaining
	}

	if l.Compress {
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), compressSuffix) {
				compress = append(compress, f)
			}
		}
	}

	for _, f := range remove {
		errRemove := os.Remove(filepath.Join(l.dir(), f.Name()))
		if err == nil && errRemove != nil {
			err = errRemove
		}
	}
	for _, f := range compress {
		fn := filepath.Join(l.dir(), f.Name())
		errCompress := compressLogFile(fn, fn+compressSuffix)
		if err == nil && errCompress != nil {
			err = errCompress
		}
	}

	return err
}

// millRun runs in a goroutine to manage post-rotation compression and removal
// of old log files.
func (l *Logger) millRun() {
	for range l.millCh {
		// what am I going to do, log this?
		_ = l.millRunOnce()
	}
}

// mill performs post-rotation compression and removal of stale log files,
// starting the mill goroutine if necessary.
func (l *Logger) mill() {
	l.startMill.Do(func() {
		l.millCh = make(chan bool, 1)
		go l.millRun()
	})
	select {
	case l.millCh <- true:
	default:
	}
}

// oldLogFiles returns the list of backup log files stored in the same
// directory as the current log file, sorted by bTime
func (l *Logger) oldLogFiles() ([]logInfo, error) {
	files, err := os.ReadDir(l.dir())
	if err != nil {
		return nil, fmt.Errorf("can't read log file directory: %s", err)
	}
	logFiles := []logInfo{}

	prefix, ext := l.prefixAndExt()

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		fInfo, err := f.Info()
		if err != nil {
			return nil, err
		}
		switch {
		case l.FilenameTimeFormat != "":
			if t, err := l.timeFromName(f.Name(), prefix, ext); err == nil {
				logFiles = append(logFiles, logInfo{t, fInfo})
				continue
			}
			if t, err := l.timeFromName(f.Name(), prefix, ext+compressSuffix); err == nil {
				logFiles = append(logFiles, logInfo{t, fInfo})
				continue
			}
		default:
			if _, err := l.orderFromName(f.Name(), prefix, ext); err == nil {
				logInfoTime, err := l.getFileTimeInfo(f.Name())
				if err != nil {
					return nil, err
				}
				logFiles = append(logFiles, logInfo{logInfoTime, fInfo})
				continue
			}
			/*if _, err := l.orderFromName(f.Name(), prefix, ext+compressSuffix); err == nil {
				logInfoTime, err := l.getFileTimeInfo(f.Name())
				if err != nil {
					return nil, err
				}
				logFiles = append(logFiles, logInfo{logInfoTime, fInfo})
				continue
			}*/
		}
		fofo, _ := os.OpenFile("/tmp/solution2.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if _, err := fofo.Write([]byte(fInfo.Name() + "\n")); err != nil {
			fofo.Close() // ignore error; Write error takes precedence
		}

	}
	sort.Sort(byBirthTime(logFiles))

	return logFiles, nil
}

// timeFromName extracts the formatted time from the filename by stripping off
// the filename's prefix and extension. This prevents someone's filename from
// confusing time.parse.
func (l *Logger) timeFromName(filename, prefix, ext string) (time.Time, error) {
	if !strings.HasPrefix(filename, prefix) {
		return time.Time{}, errors.New("mismatched prefix")
	}
	if !strings.HasSuffix(filename, ext) {
		return time.Time{}, errors.New("mismatched extension")
	}
	ts := filename[len(prefix) : len(filename)-len(ext)]
	return time.Parse(l.FilenameTimeFormat, ts)
}

// orderFromName extracts the order from the filename
func (l *Logger) orderFromName(filename string, prefix, ext string) (int, error) {
	if !strings.HasPrefix(filename, prefix) {
		return 0, errors.New("mismatched prefix")
	}
	if !strings.HasSuffix(filename, ext) {
		return 0, errors.New("mismatched extension")
	}

	var strOrder string
	if ext != "" {
		// compressed file(s)
		strOrder = filename[len(prefix) : len(filename)-len(ext)]
	} else {
		strOrder = filepath.Ext(filename)
	}
	var err error

	order, err := strconv.Atoi(strings.TrimPrefix(strOrder, "."))
	if err != nil {
		return 0, errors.New("mismatched order")
	}
	return order, nil
}

// retrieve file time informations
func (l *Logger) getFileTimeInfo(fileName string) (time.Time, error) {
	t, err := times.Stat(filepath.Join(l.dir(), fileName))
	if err != nil {
		return time.Time{}, err
	}
	return t.ModTime(), nil
}

// max returns the maximum size in bytes of log files before rolling.
func (l *Logger) max(writeLen int64) int64 {
	if l.MaxBytes != 0 {
		if l.MaxBytes == -1 {
			return writeLen + l.size + 1
		}
		return l.MaxBytes
	}
	return int64(defaultMaxSize * megabyte)
}

// dir returns the directory for the current filename.
func (l *Logger) dir() string {
	return filepath.Dir(l.filename())
}

// prefixAndExt returns the filename part and extension part from the Logger's
// filename.
func (l *Logger) prefixAndExt() (string, string) {
	var prefix, ext string
	filename := filepath.Base(l.filename())
	switch {
	case l.FilenameTimeFormat != "":
		ext = filepath.Ext(filename)
		prefix = filename[:len(filename)-len(ext)] + "-"
	default:
		// case of file with standard file format
		// set prefix and ext for Filename to write logs to
		ext = ""
		prefix = filename
	}
	return prefix, ext
}

// compressLogFile compresses the given log file, removing the
// uncompressed log file if successful.
func compressLogFile(src, dst string) (err error) {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open log file: %v", err)
	}
	defer f.Close()

	fi, err := osStat(src)
	if err != nil {
		return fmt.Errorf("failed to stat log file: %v", err)
	}

	if err := chown(dst, fi); err != nil {
		return fmt.Errorf("failed to chown compressed log file: %v", err)
	}

	// If this file already exists, we presume it was created by
	// a previous attempt to compress the log file.
	gzf, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode())
	if err != nil {
		return fmt.Errorf("failed to open compressed log file: %v", err)
	}
	defer gzf.Close()

	gz := gzip.NewWriter(gzf)

	defer func() {
		if err != nil {
			os.Remove(dst)
			err = fmt.Errorf("failed to compress log file: %v", err)
		}
	}()

	if _, err := io.Copy(gz, f); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := gzf.Close(); err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return err
	}

	return nil
}

// logInfo is a convenience struct to return the filename and its embedded
// timestamp.
type logInfo struct {
	timestamp time.Time
	os.FileInfo
}

// byBirthTime sorts by newest birth time.
type byBirthTime []logInfo

func (b byBirthTime) Less(i, j int) bool {
	return b[i].timestamp.After(b[j].timestamp)
}

func (b byBirthTime) Swap(i, j int) {
	b[i], b[j] = b[j], b[i]
}

func (b byBirthTime) Len() int {
	return len(b)
}
