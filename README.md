# go-logrotate
## go-logrotate is a Go package for writing logs to rolling files.

go-logrotate is based on [lumberjack](https://github.com/natefinch/lumberjack).

to import go-logrotate :

    import "github.com/fahedouch/go-logrotate"

go-logrotate add new features to Lumberjack:
- Supporting MaxBytes to specify the log size in bytes.
- Supporting unlimited MaxBytes with `-1`.
- Supporting multiple backups file names :
  - standard file name : `foo.log.1`
  - time file name: `foo-2014-05-04T14-44-33.555.log`


## Example

To use go-logrotate with the standard library's log package and with file name standard format, just pass it into the SetOutput function when your application starts.

Code:

```
log.SetOutput(&logrotate.Logger{
    Filename:   "/var/log/myapp/foo.log",
    MaxBytes:    500, // bytes
    MaxBackups: 3,
    MaxAge:     28, //days
    Compress:   true, // disabled by default
})
```

To use go-logrotate with file name time format.
File name time format takes precedence over the standard format.

```
log.SetOutput(&logrotate.Logger{
    Filename:   "/var/log/myapp/foo.log",
    FilenameTimeFormat: "2006-01-02T15-04-05.000",
    MaxBytes:    500, // bytes
    MaxBackups: 3,
    MaxAge:     28, //days
    Compress:   true, // disabled by default
})
```
