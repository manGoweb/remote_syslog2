package main

import (
	"net"
	"os"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/ActiveState/tail"
	"github.com/howbazaar/loggo"
	"github.com/papertrail/remote_syslog2/syslog"
	"github.com/papertrail/remote_syslog2/utils"
)

var log = loggo.GetLogger("")

// Tails a single file
func tailOne(file string, excludePatterns []*regexp.Regexp, logger *syslog.Logger, wr *WorkerRegistry, severity syslog.Priority, facility syslog.Priority, poll bool, tag string) {
	defer wr.Remove(file)
	wr.Add(file)
	tailConfig := tail.Config{ReOpen: true, Follow: true, MustExist: true, Poll: poll, Location: &tail.SeekInfo{0, os.SEEK_END}}

	t, err := tail.TailFile(file, tailConfig)

	if err != nil {
		log.Errorf("%s", err)
		return
	}

	reGroup := regexp.MustCompile(`^/srv/([^/]+)/`)
	matches := reGroup.FindAllStringSubmatch(file, 1)
	groupName := ""
	if len(matches) > 0 {
		groupName = matches[0][1]
	}
	
	if (tag == "") {
		tag = "-"
	}

	// remove log extension, if present
	// files are info.log, exception.log etc
	reExtension := regexp.MustCompile(`\.log$`)
	strSeverity := reExtension.ReplaceAllString(path.Base(file), "")

	reTimePrefix := regexp.MustCompile(`^\[\d+-\d+-\d+ \d+-\d+-\d+\] `)

	for line := range t.Lines {
		// remove time prefix
		line.Text = reTimePrefix.ReplaceAllString(line.Text, "")
		// append groupName and severity name as tags
		line.Text = fmt.Sprintf("%s: %s: %s", groupName, strSeverity, line.Text)

		if !matchExps(line.Text, excludePatterns) {
			logger.Packets <- syslog.Packet{
				Severity: severity,
				Facility: facility,
				Time:     time.Now(),
				Hostname: logger.ClientHostname,
				Tag:      tag,
				Message:  line.Text,
			}
			log.Tracef("Forwarding: %s", line.Text)
		} else {
			log.Tracef("Not Forwarding: %s", line.Text)
		}

	}

	log.Errorf("Tail worker executed abnormally")
}

// Tails files speficied in the globs and re-evaluates the globs
// at the specified interval
func tailFiles(globs []LogFile, excludedFiles []*regexp.Regexp, excludePatterns []*regexp.Regexp, interval RefreshInterval, logger *syslog.Logger, severity syslog.Priority, facility syslog.Priority, poll bool) {
	wr := NewWorkerRegistry()
	log.Debugf("Evaluating globs every %s", interval.Duration)
	logMissingFiles := true
	for {
		globFiles(globs, excludedFiles, excludePatterns, logger, &wr, logMissingFiles, severity, facility, poll)
		time.Sleep(interval.Duration)
		logMissingFiles = false
	}
}

//
func globFiles(globs []LogFile, excludedFiles []*regexp.Regexp, excludePatterns []*regexp.Regexp, logger *syslog.Logger, wr *WorkerRegistry, logMissingFiles bool, severity syslog.Priority, facility syslog.Priority, poll bool) {
	log.Debugf("Evaluating file globs")
	for _, glob := range globs {

		tag := glob.Tag
		files, err := filepath.Glob(utils.ResolvePath(glob.Path))

		if err != nil {
			log.Errorf("Failed to glob %s: %s", glob.Path, err)
		} else if files == nil && logMissingFiles {
			log.Errorf("Cannot forward %s, it may not exist", glob.Path)
		}

		for _, file := range files {
			switch {
			case wr.Exists(file):
				log.Debugf("Skipping %s because it is already running", file)
			case matchExps(file, excludedFiles):
				log.Debugf("Skipping %s because it is excluded by regular expression", file)
			default:
				log.Infof("Forwarding %s", file)
				go tailOne(file, excludePatterns, logger, wr, severity, facility, poll, tag)
			}
		}
	}
}

// Evaluates each regex against the string. If any one is a match
// the function returns true, otherwise it returns false
func matchExps(value string, expressions []*regexp.Regexp) bool {
	for _, exp := range expressions {
		if exp.MatchString(value) {
			return true
		}
	}
	return false
}

func main() {
	cm := NewConfigManager()

	if cm.Daemonize() {
		utils.Daemonize(cm.DebugLogFile(), cm.PidFile())
	}
	utils.AddSignalHandlers()
	loggo.ConfigureLoggers(cm.LogLevels())

	raddr := net.JoinHostPort(cm.DestHost(), strconv.Itoa(cm.DestPort()))
	log.Infof("Connecting to %s over %s", raddr, cm.DestProtocol())
	logger, err := syslog.Dial(cm.Hostname(), cm.DestProtocol(), raddr, cm.RootCAs(), cm.ConnectTimeout(), cm.WriteTimeout(), cm.TcpMaxLineLength())

	if err != nil {
		log.Errorf("Cannot connect to server: %v", err)
	}

	go tailFiles(cm.Files(), cm.ExcludeFiles(), cm.ExcludePatterns(), cm.RefreshInterval(), logger, cm.Severity(), cm.Facility(), cm.Poll())

	for err = range logger.Errors {
		log.Errorf("Syslog error: %v", err)
	}
}
