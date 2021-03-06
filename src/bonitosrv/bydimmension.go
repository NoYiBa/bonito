package main

import (
	"bonitosrv/elasticsearch"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"
)

// Type grouping the methods of this API end point
type ByDimensionApi struct {
	es    *elasticsearch.Elasticsearch
	Index string
}

func NewByDimensionApi(index string) *ByDimensionApi {
	return &ByDimensionApi{
		es:    elasticsearch.NewElasticsearch(),
		Index: index,
	}
}

type ByDimensionRequest struct {
	Timerange
	Metrics          []string
	HistogramMetrics []string `json:"histogram_metrics"`
	Config           struct {
		Primary_dimension   string
		Secondary_dimension string
		Responsetime_field  string
		Status_field        string
		Status_value_ok     string
		Count_field         string
		Percentiles         []float32
		Histogram_points    int
	}
}

// Fills in the request with an omitted configuration options that
// have defaults.
func (api *ByDimensionApi) setRequestDefaults(req *ByDimensionRequest) {
	c := &req.Config

	if len(c.Primary_dimension) == 0 {
		c.Primary_dimension = "service"
	}
	if len(c.Secondary_dimension) == 0 {
		c.Secondary_dimension = "host"
	}
	if len(c.Responsetime_field) == 0 {
		c.Responsetime_field = "responsetime"
	}
	if len(c.Status_field) == 0 {
		c.Status_field = "status"
	}
	if len(c.Status_value_ok) == 0 {
		c.Status_value_ok = "ok"
	}
	if len(c.Count_field) == 0 {
		c.Count_field = "count"
	}
	if len(c.Percentiles) == 0 {
		c.Percentiles = []float32{50, 90, 99, 99.5}
	}
	if c.Histogram_points == 0 {
		c.Histogram_points = 10
	}

	if req.Timerange.IsZero() {
		req.Timerange.From = JsTime(time.Now().Add(-1 * time.Hour))
		req.Timerange.To = JsTime(time.Now())
	}
}

// ByDimensionResponse is the structure that gets marshaled to JSON
// and sent to the client in response to a bydimension query.
type ByDimensionResponse struct {
	Status  string             `json:"status"`
	Primary []PrimaryDimension `json:"primary"`
}

type PrimaryDimension struct {
	Name         string                      `json:"name"`
	Metrics      map[string]float32          `json:"metrics"`
	Hist_metrics map[string][]HistogramValue `json:"hist_metrics"`
}

type HistogramValue struct {
	Ts    JsTime  `json:"ts"`
	Value float32 `json:"value"`
}

// EsByDimensionReq is the structure that gets marshaled to JSON
// and is sent to Elasticsearch.
type EsByDimensionReq struct {
	Aggs struct {
		Primary struct {
			Terms struct {
				Field string `json:"field"`
				Size  int    `json:"size"`
			} `json:"terms"`
			Aggs map[string]interface{} `json:"aggs"`
		} `json:"primary"`
	} `json:"aggs"`
}

func (api *ByDimensionApi) buildRequestAggs(req *ByDimensionRequest) (*MapStr, error) {
	aggs := MapStr{}
	for _, metric := range req.Metrics {
		switch metric {
		case "volume":
			aggs["volume"] = MapStr{
				"sum": MapStr{
					"field": req.Config.Count_field,
				},
			}
		case "rt_max", "rt_avg":
			aggs["rt_stats"] = MapStr{
				"stats": MapStr{
					"field": req.Config.Responsetime_field,
				},
			}
		case "rt_percentiles":
			aggs["rt_percentiles"] = MapStr{
				"percentiles": MapStr{
					"field":    req.Config.Responsetime_field,
					"percents": req.Config.Percentiles,
				},
			}
		case "secondary_count":
			aggs["secondary_card"] = MapStr{
				"cardinality": MapStr{
					"field": req.Config.Secondary_dimension,
				},
			}
		case "errors_rate":
			aggs["errors_count"] = MapStr{
				"filter": MapStr{
					"not": MapStr{
						"term": MapStr{
							req.Config.Status_field: req.Config.Status_value_ok,
						},
					},
				},
				"aggs": MapStr{
					"count": MapStr{
						"sum": MapStr{
							"field": "count",
						},
					},
				},
			}
			// make sure the volume is there
			aggs["volume"] = MapStr{
				"sum": MapStr{
					"field": req.Config.Count_field,
				},
			}
		default:
			return nil, fmt.Errorf("Unknown metric name '%s'", metric)
		}
	}

	return &aggs, nil
}

// Returns the Timerange interval as string that can be passed to the
// Elasticseach date_histogram field. For example, 613.234s is a valid
// interval. The interval is computed in such a way so that there will
// be approximately the given number of points in the histogram.
func computeHistogramInterval(tr *Timerange, points int) string {

	// the bucket interval in seconds (can be a float)
	total_interval := time.Time(tr.To).Sub(time.Time(tr.From))
	interval_secs := float32(int64(total_interval)/int64(points)/int64(1e6)) / 1000
	return fmt.Sprintf("%.3fs", interval_secs)
}

func (api *ByDimensionApi) buildRequestHistogramAggs(req *ByDimensionRequest) (*MapStr, error) {

	interval := computeHistogramInterval(&req.Timerange, req.Config.Histogram_points)

	aggs := MapStr{}
	for _, metric := range req.HistogramMetrics {
		switch metric {
		case "volume":
			aggs["volume_hist"] = MapStr{
				"date_histogram": MapStr{
					"field":    "timestamp",
					"interval": interval,
				},
				"aggs": MapStr{
					"volume": MapStr{
						"sum": MapStr{
							"field": req.Config.Count_field,
						},
					},
				},
			}
		default:
			return nil, fmt.Errorf("Unknown histogram metric name '%s'", metric)
		}

	}
	return &aggs, nil
}

func (api *ByDimensionApi) bucketToPrimary(req *ByDimensionRequest,
	bucket map[string]json.RawMessage) (*PrimaryDimension, error) {

	var primary PrimaryDimension

	err := json.Unmarshal(bucket["key"], &primary.Name)
	if err != nil {
		return nil, err
	}

	// transform metrics
	primary.Metrics = map[string]float32{}
	for _, metric := range req.Metrics {
		switch metric {
		case "volume":
			var volume struct {
				Value float32
			}

			err = json.Unmarshal(bucket["volume"], &volume)
			if err != nil {
				return nil, err
			}

			primary.Metrics["volume"] = volume.Value
		case "rt_max":
		case "rt_avg":
			var stats struct {
				Max float32
				Avg float32
			}
			err = json.Unmarshal(bucket["rt_stats"], &stats)
			if err != nil {
				return nil, err
			}

			primary.Metrics["rt_max"] = stats.Max
			primary.Metrics["rt_avg"] = stats.Avg

		case "rt_percentiles":
			var percentiles struct {
				Values map[string]float32
			}
			err = json.Unmarshal(bucket["rt_percentiles"], &percentiles)
			for key, value := range percentiles.Values {
				primary.Metrics[fmt.Sprintf("rt_%sp", key)] = value
			}

		case "secondary_count":
			var secondary struct {
				Value float32
			}

			err = json.Unmarshal(bucket["secondary_card"], &secondary)
			if err != nil {
				return nil, err
			}

			primary.Metrics["secondary_count"] = secondary.Value
		case "errors_rate":
			var errors_count struct {
				Count struct {
					Value float32
				}
			}
			var volume1 struct {
				Value float32
			}

			err = json.Unmarshal(bucket["errors_count"], &errors_count)
			if err != nil {
				return nil, err
			}
			err = json.Unmarshal(bucket["volume"], &volume1)
			if err != nil {
				return nil, err
			}

			primary.Metrics["errors_rate"] = errors_count.Count.Value /
				volume1.Value
		}
	}

	// transform histogram metrics
	primary.Hist_metrics = map[string][]HistogramValue{}
	for _, metric := range req.HistogramMetrics {
		switch metric {
		case "volume":
			var volume_hist struct {
				Buckets []struct {
					Key_as_string elasticsearch.Time
					Volume        struct {
						Value float32
					}
				}
			}

			err = json.Unmarshal(bucket["volume_hist"], &volume_hist)
			if err != nil {
				return nil, err
			}

			values := []HistogramValue{}

			for _, bucket := range volume_hist.Buckets {
				values = append(values, HistogramValue{
					Ts:    JsTime(bucket.Key_as_string),
					Value: bucket.Volume.Value,
				})
			}

			primary.Hist_metrics["volume"] = values
		}
	}

	return &primary, nil
}

func (api *ByDimensionApi) Query(req *ByDimensionRequest) (*ByDimensionResponse, int, error) {

	var esreq EsByDimensionReq
	es := elasticsearch.NewElasticsearch()

	api.setRequestDefaults(req)

	primary := &esreq.Aggs.Primary
	primary.Terms.Field = req.Config.Primary_dimension

	// TODO: set filters

	aggs, err := api.buildRequestAggs(req)
	if err != nil {
		return nil, 400, err
	}
	primary.Aggs = *aggs

	aggs, err = api.buildRequestHistogramAggs(req)
	if err != nil {
		return nil, 400, err
	}
	for k, v := range *aggs {
		primary.Aggs[k] = v
	}

	// up to here we assume there are client errors, from here on
	// it's on us.

	objreq, err := json.Marshal(&esreq)
	if err != nil {
		return nil, 500, err
	}

	//fmt.Println("Objreq=", string(objreq))

	resp, err := es.Search(api.Index, "?search_type=count",
		string(objreq))
	if err != nil {
		return nil, 500, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, 500, err
	}

	var answ struct {
		Aggregations struct {
			Primary struct {
				Buckets []map[string]json.RawMessage
			}
		}
	}

	err = json.Unmarshal(body, &answ)
	if err != nil {
		return nil, 500, err
	}

	var response ByDimensionResponse
	response.Primary = []PrimaryDimension{}

	for _, bucket := range answ.Aggregations.Primary.Buckets {

		primary, err := api.bucketToPrimary(req, bucket)
		if err != nil {
			return nil, 500, err
		}

		response.Primary = append(response.Primary, *primary)
	}

	// if we got so far, we're successful
	response.Status = "ok"

	return &response, 200, nil
}
