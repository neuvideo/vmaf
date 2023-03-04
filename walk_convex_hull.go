package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"math"
	"math/rand"
	"time"
)

type Resolution struct {
	Height int
	Width  int
}

var resolutions = []Resolution{Resolution{1080, 1920}, Resolution{720, 1280}, Resolution{480, 854}, Resolution{360, 640}, Resolution{240, 426}, Resolution{144, 256}}

type ConvexHullPoint struct {
	Resolution Resolution
	Rate       int
	VmafScore  float64
}

func GetNextResolution(resolution Resolution) (Resolution, error) {
	for _, res := range resolutions {
		if res.Height < resolution.Height {
			return res, nil
		}
	}
	return Resolution{}, errors.New("no next resolution")
}

func GetTargetRates(rate int, numRates int) []int {
	var targetRates []int
	currentRate := rate
	targetRates = append(targetRates, currentRate)
	gap := float32(rate) / float32(numRates)
	for i := 0; i < numRates; i++ {
		currentRate = int(math.Floor(float64(currentRate) - float64(gap)))
		if currentRate < 150 {
			break
		}
		targetRates = append(targetRates, currentRate)
	}
	return targetRates
}

// EncodeVideo Encodes the video and returns the encoded file name.
func EncodeVideo(filename string, outputFilename string, resolution Resolution, rate int) error {
	fmt.Printf("Encoding %s to %d kbps and resolution %dx%d\n", filename, rate, resolution.Height, resolution.Width)
	return nil
}

func ComputeVmaf(referenceFilename string, testFilename string, result chan float64) error {
	fmt.Printf("Computing VMAF for %s and %s\n", referenceFilename, testFilename)
	rand.Seed(time.Now().UnixNano())
	vmafScore := rand.Float64() * 100.0
	result <- vmafScore
	return nil
}

func GetOptimalResolutionForRate(filename string, resolution Resolution, rate int) (ConvexHullPoint, error) {

	// Get the next resolution
	nextResolution, err := GetNextResolution(resolution)
	if err != nil {
		// Return the current resolution
		return ConvexHullPoint{Resolution: resolution, Rate: rate, VmafScore: -1.}, nil
	}

	currentResolutionEncodedFilename := fmt.Sprintf("%s_%dx%d_%dkbps", filename, resolution.Height, resolution.Width, rate)
	go func() {
		err := EncodeVideo(filename, currentResolutionEncodedFilename, resolution, rate)
		if err != nil {
			fmt.Printf("Error encoding %s to %d kbps and resolution %dx%d. Error code: %s\n", filename, rate, nextResolution.Height, nextResolution.Width, err.Error())
		}
	}()

	nextResolutionEncodedFilename := fmt.Sprintf("%s_%dx%d_%dkbps", filename, nextResolution.Height, nextResolution.Width, rate)
	go func() {
		err := EncodeVideo(filename, nextResolutionEncodedFilename, nextResolution, rate)
		if err != nil {
			fmt.Printf("Error encoding %s to %d kbps and resolution %dx%d. Error code: %s\n", filename, rate, nextResolution.Height, nextResolution.Width, err.Error())
		}
	}()

	// Wait for the encodings to finish.

	currentResVmafResult := make(chan float64, 1)
	nextResVmafResult := make(chan float64, 1)

	// Compute VMAF for the two encodings.
	go ComputeVmaf(filename, currentResolutionEncodedFilename, currentResVmafResult)
	go ComputeVmaf(filename, nextResolutionEncodedFilename, nextResVmafResult)

	// Wait for the VMAF computations to finish.
	currentResolutionVmaf := <-currentResVmafResult
	nextResolutionVmaf := <-nextResVmafResult

	fmt.Printf("VMAF for %s is %f\n", currentResolutionEncodedFilename, currentResolutionVmaf)
	fmt.Printf("VMAF for %s is %f\n", nextResolutionEncodedFilename, nextResolutionVmaf)

	// Return the resolution with the best VMAF.
	if currentResolutionVmaf > nextResolutionVmaf {
		return ConvexHullPoint{Resolution: resolution, Rate: rate, VmafScore: currentResolutionVmaf}, nil
	}

	return ConvexHullPoint{Resolution: nextResolution, Rate: rate, VmafScore: nextResolutionVmaf}, nil
}

func WalkConvexHull(filename string, resolution Resolution, rate int, numRates int) ([]ConvexHullPoint, error) {
	targetRates := GetTargetRates(rate, numRates)

	convexHull := make([]ConvexHullPoint, 0)
	currentResolution := resolution
	for _, targetRate := range targetRates {
		convexHullPoint, err := GetOptimalResolutionForRate(filename, currentResolution, targetRate)
		if err != nil {
			fmt.Printf("Error getting optimal resolution for rate %d. Error code: %s\n", targetRate, err.Error())
			return convexHull, err
		}
		convexHull = append(convexHull, convexHullPoint)
		currentResolution = convexHullPoint.Resolution
	}
	return convexHull, nil
}

func main() {
	convexHull, err := WalkConvexHull("test.mp4", Resolution{Width: 1920, Height: 1080}, 5000, 50)
	file, _ := json.MarshalIndent(convexHull, "", " ")

	_ = ioutil.WriteFile("test.json", file, 0644)
	if err != nil {
		fmt.Printf("Error walking the convex hull. Error code: %s\n", err.Error())
		return
	}
	fmt.Printf("Convex hull: %v\n", convexHull)
}
