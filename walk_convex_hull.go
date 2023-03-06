package main

import (
	"encoding/json"
	"errors"
	"fmt"
	vidio "github.com/AlexEidt/Vidio"
	"io/ioutil"
	"os"
	"os/exec"
)

type Resolution struct {
	Height int
	Width  int
}

func (resolution *Resolution) ToFilterString() string {
	return fmt.Sprintf("%dx%d", resolution.Width, resolution.Height)
}

var resolutions = []Resolution{{1080, 1920}, {720, 1280}, {540, 960}, {360, 640}, {270, 480}}

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

func IntMax(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func GetTargetRates(rate int, numRates int) []int {
	var targetRates []int

	// The minimum rate is 150kbps and the minimum gap between rates is 150kpbs.
	gap := IntMax(rate/numRates, 150)
	currentRate := 150
	targetRates = append(targetRates, currentRate)

	for i := 1; i < numRates; i++ {
		currentRate += gap
		if currentRate >= rate {
			break
		}
		targetRates = append(targetRates, currentRate)
	}

	targetRates = append(targetRates, rate)

	return targetRates
}

// EncodeVideo Encodes the video and returns the encoded file name.
func EncodeVideo(filename string, outputFilename string, resolution Resolution, rate int, success chan bool) {
	fmt.Printf("Encoding %s to %d kbps and resolution %dx%d\n", filename, rate, resolution.Height, resolution.Width)

	cmd := exec.Command("ffmpeg", "-i", filename, "-c:v", "libx264", "-b:v", fmt.Sprintf("%dk", rate), "-s", fmt.Sprintf("%dx%d", resolution.Width, resolution.Height), outputFilename)
	fmt.Printf("Executing command: %s\n", cmd.String())
	err := cmd.Run()
	if err != nil {
		fmt.Printf("Error encoding video: %s\n", err.Error())
		success <- false
		return
	}

	success <- true
}

func ParseVmafLogFile(logPath string) float64 {
	jsonFile, err := os.Open(logPath)
	if err != nil {
		fmt.Printf("Error opening log file: %s\n", err.Error())
		return -1.0
	}
	defer jsonFile.Close()
	byteValue, _ := ioutil.ReadAll(jsonFile)

	var result map[string]interface{}
	json.Unmarshal([]byte(byteValue), &result)

	fmt.Println(result["vmaf"])
	return 0.0
}

func ComputeVmaf(referenceFilename string, referenceResolution Resolution, testFilename string, result chan float64) {
	fmt.Printf("Computing VMAF for %s and %s\n", referenceFilename, testFilename)
	// Upscale the test video to the reference resolution if necessary, then compute the vmaf score.

	// Compute the VMAF score.
	logPath := fmt.Sprintf("%s.json", testFilename)

	filterCmd := fmt.Sprintf("[0:v]scale=%s:flags=bicubic:[main];[main][1:v]libvmaf=n_threads=8:log_fmt=json:log_path=%s", referenceResolution.ToFilterString(), logPath)

	cmd := exec.Command("ffmpeg", "-i", testFilename, "-i", referenceFilename, "-filter_complex", filterCmd, "-f", "null", "-")
	fmt.Printf("Executing command: %s\n", cmd.String())
	err := cmd.Run()
	if err != nil {
		fmt.Printf("Error encoding video: %s\n", err.Error())
		result <- -1.0
		return
	}

	// Parse the log file.
	result <- ParseVmafLogFile(logPath)
}

func GetOptimalResolutionForRate(referenceVideoFilename string, referenceVideoResolution Resolution, rate int, candidateResolution Resolution) (ConvexHullPoint, error) {

	// Get the next candidate resolution.
	nextResolution, err := GetNextResolution(candidateResolution)
	if err != nil {
		return ConvexHullPoint{Resolution: candidateResolution, Rate: rate, VmafScore: -1.}, nil
	}

	candidateResolutionEncodedFilename := fmt.Sprintf("%s_%dx%d_%dkbps", referenceVideoFilename, candidateResolution.Height, candidateResolution.Width, rate)
	candidateResolutionEncodeSuccess := make(chan bool)
	go EncodeVideo(referenceVideoFilename, candidateResolutionEncodedFilename, candidateResolution, rate, candidateResolutionEncodeSuccess)

	nextResolutionEncodedFilename := fmt.Sprintf("%s_%dx%d_%dkbps", referenceVideoFilename, nextResolution.Height, nextResolution.Width, rate)
	nextResolutionEncodeSuccess := make(chan bool)
	go EncodeVideo(referenceVideoFilename, nextResolutionEncodedFilename, nextResolution, rate, nextResolutionEncodeSuccess)

	// Wait for the encodings to finish.
	candidateEncodeSuccess := <-candidateResolutionEncodeSuccess
	nextEncodeSuccess := <-nextResolutionEncodeSuccess
	if !candidateEncodeSuccess || !nextEncodeSuccess {
		return ConvexHullPoint{}, errors.New("failed to encode video")
	}

	candidateVmafResult := make(chan float64, 1)
	nextVmafResult := make(chan float64, 1)

	// Compute VMAF for the two encodings.
	go ComputeVmaf(referenceVideoFilename, referenceVideoResolution, candidateResolutionEncodedFilename, candidateVmafResult)
	go ComputeVmaf(referenceVideoFilename, referenceVideoResolution, nextResolutionEncodedFilename, nextVmafResult)

	// Wait for the VMAF computations to finish.
	candidateResolutionVmaf := <-candidateVmafResult
	nextResolutionVmaf := <-nextVmafResult

	if candidateResolutionVmaf < 0 || nextResolutionVmaf < 0 {
		return ConvexHullPoint{}, errors.New("failed to compute VMAF")
	}

	// Return the resolution with the best VMAF.
	if candidateResolutionVmaf > nextResolutionVmaf {
		return ConvexHullPoint{Resolution: candidateResolution, Rate: rate, VmafScore: candidateResolutionVmaf}, nil
	}

	return ConvexHullPoint{Resolution: nextResolution, Rate: rate, VmafScore: nextResolutionVmaf}, nil
}

func WalkConvexHull(referenceVideoFilename string, referenceVideoResolution Resolution, referenceVideoRate int, numRates int) ([]ConvexHullPoint, error) {
	targetRates := GetTargetRates(referenceVideoRate, numRates)

	convexHull := make([]ConvexHullPoint, 0)
	currentResolution := referenceVideoResolution
	for _, targetRate := range targetRates {
		convexHullPoint, err := GetOptimalResolutionForRate(referenceVideoFilename, referenceVideoResolution, targetRate, currentResolution)
		if err != nil {
			fmt.Printf("Error getting optimal resolution for rate %d. Error code: %s\n", targetRate, err.Error())
			return convexHull, err
		}
		convexHull = append(convexHull, convexHullPoint)
		currentResolution = convexHullPoint.Resolution
	}
	return convexHull, nil
}

func GetVideoResolutionAndBitrate(filename string) (Resolution, int) {
	resolution := Resolution{}
	rate := -1

	video, err := vidio.NewVideo(filename)
	if err != nil {
		fmt.Printf("Error opening video %s. Error code: %s\n", filename, err.Error())
		return resolution, rate
	}
	resolution.Width = video.Width()
	resolution.Height = video.Height()
	rate = video.Bitrate()
	return resolution, rate
}

//func main() {
//	filenames := []string{"test.mp4", "test2.mp4"}
//
//	for _, filename := range filenames {
//		resolution, rate := GetVideoResolutionAndBitrate(filename)
//		if resolution.Height > 1080 {
//			fmt.Printf("Video %s has resolution %dx%d. Skipping.\n", filename, resolution.Height, resolution.Width)
//			continue
//		}
//
//		convexHull, err := WalkConvexHull(filename, resolution, rate, 20)
//		if err != nil {
//			fmt.Printf("Error walking convex hull for %s. Error code: %s\n", filename, err.Error())
//			return
//		}
//		fmt.Printf("Convex hull for %s: %v\n", filename, convexHull)
//	}
//}

func main() {
	score := make(chan float64, 1)
	go ComputeVmaf("test.mp4", Resolution{Height: 480, Width: 720}, "test_480x360_150kbps.mp4", score)
	fmt.Printf("Score: %f\n", <-score)
}
