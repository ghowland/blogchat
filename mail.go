package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// mailTimeout limits the total time of one send operation.
const mailTimeout = 10 * time.Second

// SendMail delivers one message. The function uses STARTTLS when the server
// offers it. There is no authentication, because the design uses a local
// relay on the same machine.
func (app *App) SendMail(recipient, subject, body string) error {
	conf := app.Conf()

	conn, err := net.DialTimeout("tcp", conf.SMTPHost, mailTimeout)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	conn.SetDeadline(time.Now().Add(mailTimeout))

	host, _, err := net.SplitHostPort(conf.SMTPHost)
	if err != nil {
		host = conf.SMTPHost
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	if found, _ := client.Extension("STARTTLS"); found {
		if err := client.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if err := client.Mail(conf.MailFrom); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := client.Rcpt(recipient); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := writer.Write(buildMessage(conf, recipient, subject, body)); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return client.Quit()
}

// buildMessage makes a plain text message with the standard headers.
// The header values are cleaned, so that no input can add a header line.
func buildMessage(conf *Config, recipient, subject, body string) []byte {
	var buf strings.Builder
	fmt.Fprintf(&buf, "From: %s\r\n", cleanHeader(conf.MailFrom))
	fmt.Fprintf(&buf, "To: %s\r\n", cleanHeader(recipient))
	fmt.Fprintf(&buf, "Subject: %s\r\n", cleanHeader(subject))
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("Auto-Submitted: auto-generated\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(buf.String())
}

func cleanHeader(text string) string {
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\n", "")
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

// SendLoginMail sends the one-time login link.
func (app *App) SendLoginMail(recipient, raw string) error {
	conf := app.Conf()
	body := fmt.Sprintf(
		"Open this link to sign in to %s.\n\n%s/l/%s\n\n"+
			"The link is valid for 15 minutes and works one time only.\n"+
			"If you did not request this link, delete this message.\n",
		conf.SiteName, conf.SiteURL, raw)
	return app.SendMail(recipient, conf.SiteName+" sign-in link", body)
}

// SendInviteMail tells a new member that an account exists.
func (app *App) SendInviteMail(recipient, handle, inviter, raw string) error {
	conf := app.Conf()
	body := fmt.Sprintf(
		"%s invited you to %s.\n\nYour handle is %s.\n\n"+
			"Open this link to sign in.\n\n%s/l/%s\n\n"+
			"The link is valid for 7 days and works one time only.\n"+
			"After that, request a new link with your email address at %s/\n",
		inviter, conf.SiteName, handle, conf.SiteURL, raw, conf.SiteURL)
	return app.SendMail(recipient, "Invitation to "+conf.SiteName, body)
}

