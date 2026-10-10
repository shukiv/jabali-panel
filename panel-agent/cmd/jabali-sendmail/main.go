// Command jabali-sendmail is the sendmail-compatible submission shim PHP's
// mail() executes (JAB-230). install.sh removes every traditional MTA in
// favour of Stalwart, which left /usr/sbin/sendmail dangling and broke
// wp_mail() fleet-wide. The FPM pool template points sendmail_path here.
//
// It runs as the tenant uid. The panel provisions per-domain SendOnly relay
// credentials under /etc/jabali-panel/sendmail/<user>/ (0640 root:<usergroup>)
// and the shim submits to Stalwart on 127.0.0.1:587 (STARTTLS, verified) with
// the envelope sender forced to the credential identity.
//
// When the admin sends website mail through a smarthost instead (GH #2056,
// ADR 0174), the shim hands the message to the jabali-mailrelay socket.
package main

import (
	"fmt"
	"log/syslog"
	"os"
	"os/user"
	"strconv"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/mailrelay"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/sendmailshim"
)

const (
	credRoot   = "/etc/jabali-panel/sendmail"
	submitAddr = "127.0.0.1:587"
)

func main() {
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jabali-sendmail: %v\n", err)
	}
	os.Exit(sendmailshim.ExitCode(err))
}

func run() error {
	opts, err := sendmailshim.ParseArgs(os.Args[1:])
	if err != nil {
		return err
	}

	uid := os.Getuid()
	username := strconv.Itoa(uid)
	if u, lerr := user.LookupId(username); lerr == nil {
		username = u.Username
	}

	msg, err := sendmailshim.ParseMessage(os.Stdin, opts.ReadRecipientsFromHeaders)
	if err != nil {
		logLine(username, "", "", 0, err)
		return err
	}

	recipients := append([]string{}, opts.Recipients...)
	if opts.ReadRecipientsFromHeaders {
		recipients = append(recipients, msg.HeaderRecipients...)
	}

	// GH #2056: with a smarthost selected, the relay sends the message. It
	// holds the smarthost login and picks the sender from this process's UID,
	// so nothing here needs a credential.
	if mailrelay.ReadMode(mailrelay.ModePath) == mailrelay.ModeSmarthost {
		err = mailrelay.Submit(mailrelay.SocketPath, recipients, msg.Raw)
		logLine(username, msg.FromDomain, "smarthost-relay", len(recipients), err)
		return err
	}

	cred, err := sendmailshim.LoadCred(credRoot+"/"+username, msg.FromDomain)
	if err != nil {
		logLine(username, msg.FromDomain, "", len(recipients), err)
		return err
	}

	body := sendmailshim.EnsureSender(msg, cred.Email)
	err = sendmailshim.Submit(submitAddr, cred, recipients, body)
	logLine(username, msg.FromDomain, cred.Email, len(recipients), err)
	return err
}

// logLine best-effort syslogs one line per attempt; operators debug "why did
// the form not send" from here. Never includes secrets or message content.
func logLine(user, fromDomain, identity string, rcpts int, err error) {
	w, serr := syslog.New(syslog.LOG_MAIL|syslog.LOG_INFO, "jabali-sendmail")
	if serr != nil {
		return
	}
	defer w.Close()
	line := sendmailshim.FormatLog(user, fromDomain, identity, rcpts, err)
	if err != nil {
		_ = w.Err(line)
		return
	}
	_ = w.Info(line)
}
