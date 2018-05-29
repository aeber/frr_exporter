package collector

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
)

// The namespace used by all metrics.
const namespace = "frr"

var (
	frrTotalScrapeCount = 0.0
	frrTotalErrorCount  = 0

	frrScrapesTotal   = prometheus.NewDesc(namespace+"_scrapes_total", "Total number of times FRR has been scraped.", nil, nil)
	frrScrapeErrTotal = prometheus.NewDesc(namespace+"_scrape_errors_total", "Total number of errors from collector scrapes.", nil, nil)
	frrScrapeDuration = prometheus.NewDesc(namespace+"_scrape_duration_seconds", "Time it took for a collector's scrape to complete.", []string{"collector"}, nil)
	frrCollectorUp    = prometheus.NewDesc(namespace+"_collector_up", "Whether the collector's last scrape was successful (1 = successful, 0 = unsuccessful).", []string{"collector"}, nil)

	frrUp = prometheus.NewDesc(namespace+"_up", "Whether FRR is currently up.", nil, nil)
)

// CLIHelper is used to populate flags.
type CLIHelper interface {
	// What the collector does.
	Help() string

	// Name of the collector.
	Name() string

	// Whether or not the collector is enabled by default.
	EnabledByDefault() bool
}

// CollectErrors is used to collect collector errors.
type CollectErrors interface {
	CollectErrors() []error
}

// Exporters contains a slice of Collectors.
type Exporters struct {
	Collectors []Collector
}

// Collector contains everything needed to collect from a collector.
type Collector struct {
	CLIHelper     CLIHelper
	PromCollector prometheus.Collector
	Errors        CollectErrors
}

// NewExporter creates a new exporter.
func NewExporter(collectors []Collector) *Exporters {
	return &Exporters{Collectors: collectors}
}

// Describe implemented as per the prometheus.Collector interface.
func (e *Exporters) Describe(ch chan<- *prometheus.Desc) {
	ch <- frrScrapesTotal
	ch <- frrScrapeErrTotal
	ch <- frrUp
	ch <- frrScrapeDuration
	ch <- frrCollectorUp
	for _, collector := range e.Collectors {
		collector.PromCollector.Describe(ch)
	}
}

// Collect implemented as per the prometheus.Collector interface.
func (e *Exporters) Collect(ch chan<- prometheus.Metric) {
	frrTotalScrapeCount++
	ch <- prometheus.MustNewConstMetric(frrScrapesTotal, prometheus.CounterValue, frrTotalScrapeCount)

	errCh := make(chan int, 1024)
	wg := &sync.WaitGroup{}
	for _, collector := range e.Collectors {
		wg.Add(1)
		go runCollector(ch, errCh, collector, wg)
	}
	wg.Wait()

	close(errCh)
	errCount := processErrors(errCh)

	// If at least one collector is successfull we can assume FRR is running, otherwise assume FRR is not running. This is
	// cheaper than executing an FRR command and is a good enough method to determine whether FRR is up.
	frrState := 0.0
	if errCount < len(e.Collectors) {
		frrState = 1
	}
	ch <- prometheus.MustNewConstMetric(frrUp, prometheus.GaugeValue, frrState)
}

func processErrors(errCh chan int) int {
	errors := 0
	for {
		_, more := <-errCh
		if !more {
			return errors
		}
		errors++
	}
}

func runCollector(ch chan<- prometheus.Metric, errCh chan<- int, collector Collector, wg *sync.WaitGroup) {
	defer wg.Done()
	startTime := time.Now()

	collector.PromCollector.Collect(ch)

	errors := collector.Errors.CollectErrors()

	if len(errors) > 0 {
		errCh <- 1
		ch <- prometheus.MustNewConstMetric(frrCollectorUp, prometheus.GaugeValue, 0, collector.CLIHelper.Name())
		for _, err := range errors {
			log.Errorf("collector \"%s\" scrape failed: %s", collector.CLIHelper.Name(), err)
		}
	} else {
		ch <- prometheus.MustNewConstMetric(frrCollectorUp, prometheus.GaugeValue, 1, collector.CLIHelper.Name())
	}
	ch <- prometheus.MustNewConstMetric(frrScrapeDuration, prometheus.GaugeValue, float64(time.Since(startTime).Seconds()), collector.CLIHelper.Name())
}
