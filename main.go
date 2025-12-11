// ContaCloud SMTP-to-SendGrid Relay
//
// Receives SMTP emails on port 25 (internal) and forwards them
// to SendGrid via HTTP API (port 443).
//
// Designed for Kubernetes environments where outbound SMTP ports
// (25, 465, 587) are blocked (e.g., DigitalOcean, GKE).
//
// Environment variables:
//   - SENDGRID_API_KEY: SendGrid API key (required)
//   - SMTP_LISTEN_ADDR: Address to listen on (default: ":25")
//   - SMTP_DOMAIN: Domain for SMTP server (default: "localhost")
//   - LOG_LEVEL: Logging level: debug, info, warn, error (default: "info")
//   - ALLOWED_SENDERS: Comma-separated list of allowed sender domains (optional)

package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/sendgrid/sendgrid-go"
	sgmail "github.com/sendgrid/sendgrid-go/helpers/mail"
)

// Config holds the relay configuration
type Config struct {
	SendGridAPIKey string
	ListenAddr     string
	Domain         string
	LogLevel       string
	AllowedSenders []string
}

// Logger levels
type LogLevel int

const (
	LogDebug LogLevel = iota
	LogInfo
	LogWarn
	LogError
)

var currentLogLevel LogLevel = LogInfo

func parseLogLevel(level string) LogLevel {
	switch strings.ToLower(level) {
	case "debug":
		return LogDebug
	case "info":
		return LogInfo
	case "warn", "warning":
		return LogWarn
	case "error":
		return LogError
	default:
		return LogInfo
	}
}

func logDebug(format string, v ...interface{}) {
	if currentLogLevel <= LogDebug {
		log.Printf("[DEBUG] "+format, v...)
	}
}

func logInfo(format string, v ...interface{}) {
	if currentLogLevel <= LogInfo {
		log.Printf("[INFO] "+format, v...)
	}
}

func logWarn(format string, v ...interface{}) {
	if currentLogLevel <= LogWarn {
		log.Printf("[WARN] "+format, v...)
	}
}

func logError(format string, v ...interface{}) {
	if currentLogLevel <= LogError {
		log.Printf("[ERROR] "+format, v...)
	}
}

// Backend implements smtp.Backend
type Backend struct {
	config *Config
}

func (bkd *Backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	remoteAddr := c.Conn().RemoteAddr().String()
	logDebug("New SMTP session from %s", remoteAddr)
	return &Session{
		config:     bkd.config,
		remoteAddr: remoteAddr,
	}, nil
}

// Session implements smtp.Session
type Session struct {
	config     *Config
	remoteAddr string
	from       string
	to         []string
}

func (s *Session) AuthPlain(username, password string) error {
	// No authentication required for internal relay
	logDebug("Auth attempt from %s (ignored - internal relay)", s.remoteAddr)
	return nil
}

func (s *Session) Mail(from string, opts *smtp.MailOptions) error {
	// Validate sender if allowed list is configured
	if len(s.config.AllowedSenders) > 0 {
		allowed := false
		fromLower := strings.ToLower(from)
		for _, domain := range s.config.AllowedSenders {
			if strings.HasSuffix(fromLower, "@"+strings.ToLower(domain)) ||
				strings.HasSuffix(fromLower, "."+strings.ToLower(domain)+">") {
				allowed = true
				break
			}
		}
		if !allowed {
			logWarn("Rejected sender %s (not in allowed list)", from)
			return fmt.Errorf("sender domain not allowed")
		}
	}

	s.from = from
	logDebug("MAIL FROM: %s", from)
	return nil
}

func (s *Session) Rcpt(to string, opts *smtp.RcptOptions) error {
	s.to = append(s.to, to)
	logDebug("RCPT TO: %s", to)
	return nil
}

func (s *Session) Data(r io.Reader) error {
	startTime := time.Now()

	// Read the entire message
	data, err := io.ReadAll(r)
	if err != nil {
		logError("Failed to read email data: %v", err)
		return fmt.Errorf("failed to read email data: %w", err)
	}

	logDebug("Received email data: %d bytes", len(data))

	// Parse the email
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		logError("Failed to parse email: %v", err)
		return fmt.Errorf("failed to parse email: %w", err)
	}

	// Extract headers
	subject := decodeHeader(msg.Header.Get("Subject"))
	from := msg.Header.Get("From")
	contentType := msg.Header.Get("Content-Type")

	logDebug("Subject: %s", subject)
	logDebug("From header: %s", from)
	logDebug("Content-Type: %s", contentType)

	// Read and parse body
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		logError("Failed to read email body: %v", err)
		return fmt.Errorf("failed to read email body: %w", err)
	}

	// Send via SendGrid
	err = s.sendViaSendGrid(from, s.to, subject, body, contentType)
	if err != nil {
		logError("Failed to send via SendGrid: %v", err)
		return err
	}

	duration := time.Since(startTime)
	logInfo("Email sent successfully: from=%s to=%v subject=%q duration=%v",
		s.from, s.to, truncate(subject, 50), duration)

	return nil
}

func (s *Session) sendViaSendGrid(from string, to []string, subject string, body []byte, contentType string) error {
	// Parse from address
	fromAddr, err := mail.ParseAddress(from)
	if err != nil {
		// Use raw address if parsing fails
		fromAddr = &mail.Address{Address: strings.Trim(from, "<>")}
	}

	// Create SendGrid message
	message := sgmail.NewV3Mail()
	message.SetFrom(sgmail.NewEmail(fromAddr.Name, fromAddr.Address))
	message.Subject = subject

	// Add recipients
	p := sgmail.NewPersonalization()
	for _, recipient := range to {
		toAddr, err := mail.ParseAddress(recipient)
		if err != nil {
			toAddr = &mail.Address{Address: strings.Trim(recipient, "<>")}
		}
		p.AddTos(sgmail.NewEmail(toAddr.Name, toAddr.Address))
	}
	message.AddPersonalizations(p)

	// Handle content based on type
	if strings.Contains(contentType, "multipart/") {
		// Parse multipart message
		err := s.handleMultipart(message, body, contentType)
		if err != nil {
			logWarn("Failed to parse multipart, sending as plain text: %v", err)
			message.AddContent(sgmail.NewContent("text/plain", string(body)))
		}
	} else if strings.Contains(contentType, "text/html") {
		message.AddContent(sgmail.NewContent("text/html", string(body)))
	} else {
		// Default to plain text
		message.AddContent(sgmail.NewContent("text/plain", string(body)))
	}

	// Send via SendGrid API
	client := sendgrid.NewSendClient(s.config.SendGridAPIKey)
	response, err := client.Send(message)
	if err != nil {
		return fmt.Errorf("sendgrid API error: %w", err)
	}

	if response.StatusCode >= 400 {
		logError("SendGrid returned error: status=%d body=%s", response.StatusCode, response.Body)
		return fmt.Errorf("sendgrid returned status %d: %s", response.StatusCode, response.Body)
	}

	logDebug("SendGrid response: status=%d", response.StatusCode)
	return nil
}

func (s *Session) handleMultipart(message *sgmail.SGMailV3, body []byte, contentType string) error {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return err
	}

	if !strings.HasPrefix(mediaType, "multipart/") {
		return fmt.Errorf("not a multipart message")
	}

	boundary := params["boundary"]
	if boundary == "" {
		return fmt.Errorf("no boundary found")
	}

	mr := multipart.NewReader(bytes.NewReader(body), boundary)

	var textContent, htmlContent string

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		partContentType := part.Header.Get("Content-Type")
		partBody, err := io.ReadAll(part)
		if err != nil {
			continue
		}

		if strings.Contains(partContentType, "text/plain") {
			textContent = string(partBody)
		} else if strings.Contains(partContentType, "text/html") {
			htmlContent = string(partBody)
		}
	}

	// Add content (prefer HTML if available)
	if htmlContent != "" {
		message.AddContent(sgmail.NewContent("text/html", htmlContent))
	}
	if textContent != "" {
		message.AddContent(sgmail.NewContent("text/plain", textContent))
	}

	if textContent == "" && htmlContent == "" {
		return fmt.Errorf("no text or html content found")
	}

	return nil
}

func (s *Session) Reset() {
	s.from = ""
	s.to = nil
	logDebug("Session reset")
}

func (s *Session) Logout() error {
	logDebug("Session logout from %s", s.remoteAddr)
	return nil
}

// Helper functions

func decodeHeader(header string) string {
	dec := new(mime.WordDecoder)
	decoded, err := dec.DecodeHeader(header)
	if err != nil {
		return header
	}
	return decoded
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func loadConfig() (*Config, error) {
	config := &Config{
		SendGridAPIKey: os.Getenv("SENDGRID_API_KEY"),
		ListenAddr:     os.Getenv("SMTP_LISTEN_ADDR"),
		Domain:         os.Getenv("SMTP_DOMAIN"),
		LogLevel:       os.Getenv("LOG_LEVEL"),
	}

	if config.SendGridAPIKey == "" {
		return nil, fmt.Errorf("SENDGRID_API_KEY environment variable is required")
	}

	if config.ListenAddr == "" {
		config.ListenAddr = ":25"
	}

	if config.Domain == "" {
		config.Domain = "localhost"
	}

	if config.LogLevel == "" {
		config.LogLevel = "info"
	}

	// Parse allowed senders
	allowedSenders := os.Getenv("ALLOWED_SENDERS")
	if allowedSenders != "" {
		for _, sender := range strings.Split(allowedSenders, ",") {
			sender = strings.TrimSpace(sender)
			if sender != "" {
				config.AllowedSenders = append(config.AllowedSenders, sender)
			}
		}
	}

	return config, nil
}

func main() {
	// Load configuration
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// Set log level
	currentLogLevel = parseLogLevel(config.LogLevel)

	// Create backend
	be := &Backend{config: config}

	// Create SMTP server
	s := smtp.NewServer(be)
	s.Addr = config.ListenAddr
	s.Domain = config.Domain
	s.AllowInsecureAuth = true
	s.MaxMessageBytes = 25 * 1024 * 1024 // 25 MB
	s.MaxRecipients = 50
	s.ReadTimeout = 30 * time.Second
	s.WriteTimeout = 30 * time.Second

	// Print startup info
	logInfo("===========================================")
	logInfo("ContaCloud SMTP-to-SendGrid Relay")
	logInfo("===========================================")
	logInfo("Listen address: %s", config.ListenAddr)
	logInfo("Domain: %s", config.Domain)
	logInfo("Log level: %s", config.LogLevel)
	if len(config.AllowedSenders) > 0 {
		logInfo("Allowed senders: %v", config.AllowedSenders)
	} else {
		logInfo("Allowed senders: all")
	}
	logInfo("Max message size: 25 MB")
	logInfo("===========================================")
	logInfo("Ready to relay emails to SendGrid API")
	logInfo("===========================================")

	// Start server
	if err := s.ListenAndServe(); err != nil {
		log.Fatalf("SMTP server error: %v", err)
	}
}

// Health check endpoint could be added here if needed
// For now, Kubernetes can use TCP probe on port 25

// Metrics could be added with prometheus client if needed
