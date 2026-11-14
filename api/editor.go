package api

import (
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/TwiN/gatus/v5/config"
	"github.com/TwiN/logr"
	"github.com/gofiber/fiber/v2"
)

func registerConfigurationEditor(router fiber.Router, cfg *config.Config) {
	// Also check on each request in case the feature was disabled after routing.
	router.Use("/v1/config/editor", func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		if !config.EditorEnabled() {
			return c.SendStatus(fiber.StatusNotFound)
		}
		// JSON plus a same-origin check prevents cross-site cookie-authenticated
		// writes. Requests without Origin (CLI clients) remain supported.
		if origin := c.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			developmentOrigin := os.Getenv("ENVIRONMENT") == "dev" && origin == "http://localhost:8081"
			if !developmentOrigin && (err != nil || parsed.Host != c.Get("Host")) {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "same-origin request required"})
			}
		}
		return c.Next()
	})
	router.Get("/v1/config/editor/schema", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(config.ConfigurationSchema())
	})
	router.Get("/v1/config/editor", func(c *fiber.Ctx) error {
		snapshot, err := cfg.EditorSnapshot()
		if err != nil {
			logr.Errorf("[api.configEditor] Failed to read configuration: %v", err)
			return c.Status(500).JSON(fiber.Map{"error": "could not read configuration files"})
		}
		return c.Status(fiber.StatusOK).JSON(snapshot)
	})
	edit := func(save bool) fiber.Handler {
		return func(c *fiber.Ctx) error {
			if strings.TrimSpace(strings.Split(c.Get(fiber.HeaderContentType), ";")[0]) != fiber.MIMEApplicationJSON {
				return c.SendStatus(fiber.StatusUnsupportedMediaType)
			}
			var draft struct {
				File     string `json:"file"`
				Content  string `json:"content"`
				Revision string `json:"revision"`
			}
			if err := c.BodyParser(&draft); err != nil {
				return c.SendStatus(fiber.StatusBadRequest)
			}
			snapshot, err := cfg.EditConfiguration(draft.File, draft.Content, draft.Revision, save)
			if err != nil {
				status := fiber.StatusUnprocessableEntity
				if errors.Is(err, config.ErrEditorConflict) {
					status = fiber.StatusConflict
				}
				if errors.Is(err, config.ErrEditorFile) {
					status = fiber.StatusBadRequest
				}
				return c.Status(status).JSON(fiber.Map{"error": err.Error()})
			}
			if save {
				return c.JSON(snapshot)
			}
			return c.JSON(fiber.Map{"valid": true})
		}
	}
	router.Post("/v1/config/editor/validate", edit(false))
	router.Put("/v1/config/editor", edit(true))
}
