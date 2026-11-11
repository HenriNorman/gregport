// Command reindex rebuilds the Elasticsearch search index from Postgres.
//
// It pages all non-deleted tickets from Postgres, builds derived search
// documents, bulk-indexes them into a new versioned index (tickets-v2), then
// atomically swaps the stable "tickets" alias at it and deletes the previous
// index. Postgres remains the source of truth throughout; the index is
// entirely derived and can be rebuilt at any time.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/HenriNorman/gregport/internal/platform/config"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/search"
	"github.com/HenriNorman/gregport/internal/tickets"
)

const (
	reindexBatchSize = 500
	reindexPageSize  = 1000
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file, relying on real env vars")
	}
	cfg, err := config.LoadWorker()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		log.Fatalf("logging: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.NewPool(ctx, cfg.DSN())
	if err != nil {
		logger.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	logger.Info("connected to postgres")

	esClient, err := search.NewClient(search.ClientConfig{
		URL:      cfg.ElasticsearchURL,
		Index:    cfg.ElasticsearchIndex,
		Username: cfg.ElasticsearchUsername,
		Password: cfg.ElasticsearchPassword,
		APIKey:   cfg.ElasticsearchAPIKey,
		Timeout:  cfg.ElasticsearchTimeout,
	})
	if err != nil {
		log.Fatalf("search client: %v", err)
	}

	indexMgr := search.NewIndexManager(esClient)
	if err := indexMgr.Bootstrap(ctx); err != nil {
		log.Fatalf("elasticsearch bootstrap: %v", err)
	}
	logger.Info("elasticsearch index bootstrapped")

	oldIndex, err := indexMgr.AliasTarget(ctx)
	if err != nil {
		log.Fatalf("resolve alias target: %v", err)
	}
	if oldIndex != "" {
		logger.Info("current alias target", "index", oldIndex)
	}

	newIndex := nextIndexName(esClient, oldIndex)
	if newIndex == oldIndex {
		log.Fatalf("refusing to reindex into the current alias target %q", oldIndex)
	}
	exists, err := indexMgr.IndexExists(ctx, newIndex)
	if err != nil {
		log.Fatalf("check new index: %v", err)
	}
	if exists {
		logger.Warn("new index already exists, deleting", "index", newIndex)
		if err := indexMgr.DeleteIndex(ctx, newIndex); err != nil {
			log.Fatalf("delete existing new index: %v", err)
		}
	}
	if err := indexMgr.CreateIndex(ctx, newIndex); err != nil {
		log.Fatalf("create new index: %v", err)
	}
	logger.Info("created new index", "index", newIndex)

	docRepo := search.NewDocumentRepository(pool)
	ticketRepo := tickets.NewRepository(pool)

	if err := reindexAll(ctx, ticketRepo, docRepo, esClient, newIndex, logger); err != nil {
		log.Fatalf("reindex failed: %v", err)
	}
	logger.Info("reindex complete")

	if err := indexMgr.SwapAlias(ctx, oldIndex, newIndex); err != nil {
		log.Fatalf("swap alias: %v", err)
	}
	logger.Info("alias swapped", "from", oldIndex, "to", newIndex)

	if oldIndex != "" && oldIndex != newIndex {
		if err := indexMgr.DeleteIndex(ctx, oldIndex); err != nil {
			logger.Warn("delete old index", "index", oldIndex, "error", err)
		} else {
			logger.Info("deleted old index", "index", oldIndex)
		}
	}

	logger.Info("reindex finished successfully")
}

// nextIndexName picks a fresh concrete index name for the rebuild. When the
// current alias target is "tickets-vN", the next generation is "tickets-vN+1";
// otherwise it falls back to a timestamped name. This makes reindex repeatable
// without ever deleting the index that is currently serving searches.
func nextIndexName(client *search.Client, current string) string {
	prefix := client.Alias() + "-v"
	if strings.HasPrefix(current, prefix) {
		if n, err := strconv.Atoi(strings.TrimPrefix(current, prefix)); err == nil && n >= 0 {
			return client.IndexName("v" + strconv.Itoa(n+1))
		}
	}
	if current == "" {
		return client.IndexName("v1")
	}
	return client.IndexName("reindex-" + strconv.FormatInt(time.Now().Unix(), 10))
}

// reindexAll pages every non-deleted ticket from Postgres, builds its search
// document, and bulk-indexes into the given index.
func reindexAll(
	ctx context.Context,
	ticketRepo *tickets.Repository,
	docRepo *search.DocumentRepository,
	esClient *search.Client,
	index string,
	logger *slog.Logger,
) error {
	cursor := ""
	total := 0

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		refs, next, err := ticketRepo.ListAllTicketsPage(ctx, reindexPageSize, cursor)
		if err != nil {
			return fmt.Errorf("list tickets page: %w", err)
		}
		if len(refs) == 0 {
			break
		}

		// Load documents for this page and bulk-index in batches.
		batch := make([]search.TicketDocument, 0, reindexBatchSize)
		for _, ref := range refs {
			state, err := docRepo.LoadDocument(ctx, ref.OrgID, ref.ID)
			if err != nil {
				return fmt.Errorf("load document %s: %w", ref.ID, err)
			}
			if !state.Found || state.Deleted {
				continue
			}
			batch = append(batch, state.Document)

			if len(batch) >= reindexBatchSize {
				if err := bulkIndex(ctx, esClient, index, batch, logger); err != nil {
					return err
				}
				total += len(batch)
				logger.Info("indexed batch", "count", len(batch), "total", total)
				batch = batch[:0]
			}
		}
		if len(batch) > 0 {
			if err := bulkIndex(ctx, esClient, index, batch, logger); err != nil {
				return err
			}
			total += len(batch)
			logger.Info("indexed batch", "count", len(batch), "total", total)
		}

		if next == "" {
			break
		}
		cursor = next
	}

	logger.Info("all tickets indexed", "total", total)
	return nil
}

// bulkIndex sends one bulk request to Elasticsearch. Each document carries its
// ticket version as an external version, so a reindex never overwrites a
// newer document written by a concurrent sync job.
func bulkIndex(
	ctx context.Context,
	esClient *search.Client,
	index string,
	docs []search.TicketDocument,
	logger *slog.Logger,
) error {
	var buf bytes.Buffer
	for _, doc := range docs {
		meta := map[string]any{
			"index": map[string]any{
				"_index":       index,
				"_id":          doc.ID.String(),
				"version":      doc.Version,
				"version_type": "external",
			},
		}
		metaRaw, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("marshal bulk meta: %w", err)
		}
		docRaw, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("marshal bulk doc: %w", err)
		}
		buf.Write(metaRaw)
		buf.WriteByte('\n')
		buf.Write(docRaw)
		buf.WriteByte('\n')
	}

	res, err := esClient.ES().Bulk(
		bytes.NewReader(buf.Bytes()),
		esClient.ES().Bulk.WithContext(ctx),
		esClient.ES().Bulk.WithIndex(index),
		esClient.ES().Bulk.WithRefresh("true"),
	)
	if err != nil {
		return fmt.Errorf("%w: bulk request: %v", search.ErrSearchUnavailable, err)
	}
	defer res.Body.Close()
	if err := search.ResponseError(res); err != nil {
		return err
	}

	// Check for per-document errors in the bulk response.
	var body struct {
		Errors bool `json:"errors"`
		Items  []struct {
			Index struct {
				ID     string `json:"_id"`
				Status int    `json:"status"`
				Error  *struct {
					Type   string `json:"type"`
					Reason string `json:"reason"`
				} `json:"error"`
			} `json:"index"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); err != nil {
		return fmt.Errorf("decode bulk response: %w", err)
	}
	if !body.Errors {
		return nil
	}

	for _, item := range body.Items {
		if item.Index.Error != nil {
			return fmt.Errorf("bulk index %s: %s: %s", item.Index.ID, item.Index.Error.Type, item.Index.Error.Reason)
		}
	}
	return errors.New("bulk index: unknown error")
}
