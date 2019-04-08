package main

// #cgo linux CFLAGS: -I/usr/local/cuda/include
// #cgo linux LDFLAGS: -lcuda -lcudart -L/usr/local/cuda/lib64
// #include <cuda.h>
// #include <cuda_runtime.h>
// #include <cuda_profiler_api.h>
import "C"

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"

	"github.com/anthonynsimon/bild/imgio"
	"github.com/anthonynsimon/bild/transform"
	"github.com/k0kubun/pp"
	"github.com/rai-project/config"
	"github.com/rai-project/dlframework"
	"github.com/rai-project/dlframework/framework/feature"
	"github.com/rai-project/dlframework/framework/options"
	"github.com/rai-project/downloadmanager"
	"github.com/rai-project/go-mxnet/mxnet"
	nvidiasmi "github.com/rai-project/nvidia-smi"
	_ "github.com/rai-project/tracer/all"
)

var (
	batchSize   = 1
	model       = "squeezenet_v1.0"
	graph_url   = "http://s3.amazonaws.com/store.carml.org/models/mxnet/squeezenet_v1.0/squeezenet_v1.0-symbol.json" //"http://s3.amazonaws.com/store.carml.org/models/mxnet/bvlc_alexnet/bvlc_alexnet-symbol.json"
	weights_url = "http://s3.amazonaws.com/store.carml.org/models/mxnet/squeezenet_v1.0/squeezenet_v1.0-0000.params" // "http://s3.amazonaws.com/store.carml.org/models/mxnet/bvlc_alexnet/bvlc_alexnet-0000.params"
	synset_url  = "http://s3.amazonaws.com/store.carml.org/synsets/imagenet/synset.txt"
)

// convert go Image to 1-dim array
func cvtImageToNCHW1DArray(src image.Image, mean []float32) ([]float32, error) {
	if src == nil {
		return nil, fmt.Errorf("src image nil")
	}

	b := src.Bounds()
	h := b.Max.Y - b.Min.Y // image height
	w := b.Max.X - b.Min.X // image width

	res := make([]float32, 3*h*w)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := src.At(x+b.Min.X, y+b.Min.Y).RGBA()
			res[y*w+x] = float32(r>>8) - mean[0]
			res[w*h+y*w+x] = float32(g>>8) - mean[1]
			res[2*w*h+y*w+x] = float32(b>>8) - mean[2]
		}
	}

	return res, nil
}

func main() {
	dir, _ := filepath.Abs("../tmp")
	dir = filepath.Join(dir, model)
	graph := filepath.Join(dir, "squeezenet_v1.0-symbol.json")
	weights := filepath.Join(dir, "squeezenet_v1.0-0000.params")
	synset := filepath.Join(dir, "synset.txt")

	if !com.IsFile(graph) {
		if _, err := downloadmanager.DownloadInto(graph_url, dir); err != nil {
			panic(err)
		}
	}
	if !com.IsFile(weights) {
		if _, err := downloadmanager.DownloadInto(weights_url, dir); err != nil {
			panic(err)
		}
	}
	if !com.IsFile(synset) {
		if _, err := downloadmanager.DownloadInto(synset_url, dir); err != nil {
			panic(err)
		}
	}

	// load model
	symbol, err := ioutil.ReadFile(graph)
	if err != nil {
		panic(err)
	}
	params, err := ioutil.ReadFile(weights)
	if err != nil {
		panic(err)
	}

	imgDir, _ := filepath.Abs("../_fixtures")
	imagePath := filepath.Join(imgDir, "cheeseburger.jpg")

	img, err := imgio.Open(imagePath)
	if err != nil {
		panic(err)
	}

	var input []float32
	for ii := 0; ii < batchSize; ii++ {
		resized := transform.Resize(img, 224, 224, transform.Linear)
		res, err := cvtImageTo1DArray(resized, []float32{0, 0, 0}) // []float32{123, 117, 104})
		if err != nil {
			panic(err)
		}
		input = append(input, res...)
	}

	opts := options.New()

	device := options.CPU_DEVICE
	if nvidiasmi.HasGPU {
		device = options.CUDA_DEVICE

	}

	ctx := context.Background()

	in := options.Node{
		Key:   "data",
		Shape: []int{1, 3, 224, 224},
	}
	predictor, err := mxnet.New(
		ctx,
		options.WithOptions(opts),
		options.Device(device, 0),
		options.Graph(symbol),
		options.Weights(params),
		options.BatchSize(batchSize),
		options.InputNodes([]options.Node{in}),
		options.OutputNodes([]options.Node{
			options.Node{Dtype: tensor.Float32},
		}),
	)
	if err != nil {
		panic(fmt.Sprintf("+v", err))
	}
	defer predictor.Close()

  inputs := []tensor.Tensor{
		tensor.NewDense(tensor.Float32, in.Shape, tensor.WithBacking(input)),
  }
  
	C.cudaProfilerStart()

	err = predictor.Predict(ctx, inputs)
	if err != nil {
		panic(err)
	}

	C.cudaDeviceSynchronize()
	C.cudaProfilerStop()

	outputs, err := predictor.ReadPredictionOutputs(ctx)
	if err != nil {
		panic(err)
	}

  if len(outputs) != 1 {
		panic(errors.Errorf("invalid output length. got outputs of length %v", len(outputs)))
	}

	var labels []string
	f, err := os.Open(synset)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		labels = append(labels, line)
	}

	features := make([]dlframework.Features, batchSize)
	featuresLen := len(output) / batchSize

	for ii := 0; ii < batchSize; ii++ {
		rprobs := make([]*dlframework.Feature, featuresLen)
		for jj := 0; jj < featuresLen; jj++ {
			rprobs[jj] = feature.New(
				feature.ClassificationIndex(int32(jj)),
				feature.ClassificationLabel(labels[jj]),
				feature.Probability(output[ii*featuresLen+jj]),
			)
		}
		sort.Sort(dlframework.Features(rprobs))
		features[ii] = rprobs
	}

	if true {
		for i := 0; i < 1; i++ {
			results := features[i]
			top1 := results[0]
			pp.Println(top1.Probability)
			pp.Println(top1.GetClassification().GetLabel())
		}
	} else {
		_ = features
	}
}

func init() {
	config.Init(
		config.AppName("carml"),
		config.VerboseMode(true),
		config.DebugMode(true),
	)
}
