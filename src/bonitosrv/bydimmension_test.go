package main

import (
	"fmt"
	"os"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"bonitosrv/elasticsearch"
	"bonitosrv/testdata"
)

var _ = Describe("ByDimension API", func() {
	Context("Simple requests with 2 services", func() {
		var es *elasticsearch.Elasticsearch
		var index_name string
		var api *ByDimensionApi
		BeforeEach(func() {
			index_name = fmt.Sprintf("packetbeat-unittest-%v", os.Getpid())
			es = elasticsearch.NewElasticsearch()

			_, err := es.DeleteIndex(index_name)
			Expect(err).To(BeNil())
			es.Refresh(index_name)

			ts1, _ := elasticsearch.TimeParse("2015-01-02T15:04:05.000Z")
			ts2, _ := elasticsearch.TimeParse("2015-01-02T15:04:05.001Z")

			transactions := []testdata.TestTransaction{
				testdata.TestTransaction{
					Timestamp:    ts1,
					Service:      "service1",
					Host:         "Host0",
					Count:        2,
					Responsetime: 2000,
					Status:       "ok",
				},
				testdata.TestTransaction{
					Timestamp:    ts2,
					Service:      "service2",
					Host:         "Host3",
					Count:        4,
					Responsetime: 2000,
					Status:       "ok",
				},
				testdata.TestTransaction{
					Timestamp:    ts2,
					Service:      "service1",
					Host:         "host2",
					Count:        3,
					Responsetime: 2100,
					Status:       "error",
				},
			}

			err = testdata.InsertInto(es, index_name, transactions)
			Expect(err).To(BeNil())

			api = NewByDimensionApi(index_name)

		})
		AfterEach(func() {
			_, err := es.DeleteIndex(index_name)
			Expect(err).To(BeNil())
			es.Refresh(index_name)
			//fmt.Println("index name", index_name)
		})

		It("should get", func() {
			var req ByDimensionRequest
			req.Timerange.From = MustParseJsTime("now-1d")
			req.Timerange.To = MustParseJsTime("now")
			req.Metrics = []string{"volume", "rt_avg", "rt_max",
				"rt_percentiles", "secondary_count", "errors_rate"}
			req.Config.Percentiles = []float32{50, 99.995}

			resp, code, err := api.Query(&req)
			Expect(err).To(BeNil())
			Expect(code).To(Equal(200))
			Expect(len(resp.Primary)).To(Equal(2))

			services := map[string]PrimaryDimension{}
			for _, primary := range resp.Primary {
				services[primary.Name] = primary
			}

			By("correct volumes")
			Expect(services["service1"].Metrics["volume"]).To(BeNumerically("~", 5))
			Expect(services["service2"].Metrics["volume"]).To(BeNumerically("~", 4))

			By("correct response times max and avg")
			Expect(services["service1"].Metrics["rt_max"]).To(BeNumerically("~", 2100))
			Expect(services["service2"].Metrics["rt_max"]).To(BeNumerically("~", 2000))
			Expect(services["service1"].Metrics["rt_avg"]).To(BeNumerically("~", 2050))
			Expect(services["service2"].Metrics["rt_avg"]).To(BeNumerically("~", 2000))

			By("correct percentiles")
			Expect(services["service1"].Metrics["rt_50.0p"]).To(BeNumerically("~", 2050))
			Expect(services["service2"].Metrics["rt_50.0p"]).To(BeNumerically("~", 2000))
			Expect(services["service2"].Metrics["rt_99.995p"]).To(BeNumerically("~", 2000))

			By("correct hosts values")
			Expect(services["service1"].Metrics["secondary_count"]).To(BeNumerically("~", 2))
			Expect(services["service2"].Metrics["secondary_count"]).To(BeNumerically("~", 1))

			By("correct errors rate")
			Expect(services["service1"].Metrics["errors_rate"]).To(BeNumerically("~",
				0.6, 1e-6))
			Expect(services["service2"].Metrics["errors_rate"]).To(BeNumerically("~", 0))
		})

		It("should get error rate if only that is requested", func() {
			var req ByDimensionRequest
			req.Metrics = []string{"errors_rate"}

			resp, _, err := api.Query(&req)
			Expect(err).To(BeNil())
			Expect(len(resp.Primary)).To(Equal(2))

			services := map[string]PrimaryDimension{}
			for _, primary := range resp.Primary {
				services[primary.Name] = primary
			}

			Expect(services["service1"].Metrics["errors_rate"]).To(BeNumerically("~",
				0.6, 1e-6))
			Expect(services["service2"].Metrics["errors_rate"]).To(BeNumerically("~", 0))
		})

		It("should get 1.0 error rates when nothing is successful", func() {
			var req ByDimensionRequest
			req.Metrics = []string{"errors_rate"}
			req.Config.Status_value_ok = "nothing"

			resp, _, err := api.Query(&req)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(resp.Primary)).To(Equal(2))

			services := map[string]PrimaryDimension{}
			for _, primary := range resp.Primary {
				services[primary.Name] = primary
			}

			Expect(services["service1"].Metrics["errors_rate"]).To(BeNumerically("~",
				1.0, 1e-6))
			Expect(services["service2"].Metrics["errors_rate"]).To(BeNumerically("~",
				1.0))
		})

		It("should return error when the metric is not defined", func() {
			var req ByDimensionRequest
			req.Metrics = []string{"something"}
			_, code, err := api.Query(&req)
			Expect(err).To(HaveOccurred())
			Expect(code).To(Equal(400))
		})

		It("should return the histogram values for volume", func() {
			var req ByDimensionRequest

			// request a two minutes interval, a point for each minute
			req.Timerange.From = MustParseJsTime("2015-01-02T15:03:00.000Z")
			req.Timerange.To = MustParseJsTime("2015-01-02T15:05:00.000Z")
			req.Config.Histogram_points = 2
			req.HistogramMetrics = []string{"volume"}

			resp, _, err := api.Query(&req)
			Expect(err).NotTo(HaveOccurred())

			services := map[string]PrimaryDimension{}
			for _, primary := range resp.Primary {
				services[primary.Name] = primary
			}

			histMetrics1 := services["service1"].Hist_metrics["volume"]
			Expect(histMetrics1).To(HaveLen(1)) // one non-zero value
			Expect(histMetrics1[0].Value).To(BeNumerically("~", 5))
		})
	})

	Context("On an inexisting index", func() {
		It("should return an error", func() {
			api := NewByDimensionApi("no-such-index")
			var req ByDimensionRequest
			req.Metrics = []string{"volume"}

			_, code, err := api.Query(&req)
			Expect(err).To(HaveOccurred())
			Expect(code).To(Equal(500))
		})
	})

	Context("computeHistogramInterval", func() {
		It("should return 15m for a 1 hour interval and 4 points", func() {
			tr := Timerange{
				From: MustParseJsTime("now-1h"),
				To:   MustParseJsTime("now"),
			}
			Expect(computeHistogramInterval(&tr, 4)).To(Equal("900.000s"))
		})

		It("should return 1m for a 1 hour interval and 60 points", func() {
			tr := Timerange{
				From: MustParseJsTime("now-1h"),
				To:   MustParseJsTime("now"),
			}
			Expect(computeHistogramInterval(&tr, 60)).To(Equal("60.000s"))
		})
	})
})
