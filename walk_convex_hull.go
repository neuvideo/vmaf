package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	vidio "github.com/AlexEidt/Vidio"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type Resolution struct {
	Height int
	Width  int
}

func (resolution *Resolution) ToFilterString() string {
	return fmt.Sprintf("%dx%d", resolution.Width, resolution.Height)
}

var resolutions = []Resolution{{2160, 3840},
	{1440, 2560},
	{1080, 1920},
	{720, 1280},
	{540, 960},
	{480, 854},
	{432, 768},
	{360, 640},
	{342, 608},
	{270, 480},
	{144, 256}}

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

func GetTargetRates(rate int) []int {
	var targetRates []int

	// Add all rates starting at 500, in increments of 500, until we reach the rate or 10,000.
	for i := 500; i <= IntMin(rate, 10000); i += 500 {
		targetRates = append(targetRates, i)
	}

	// In reverse order
	for i := 0; i < len(targetRates)/2; i++ {
		temp := targetRates[i]
		targetRates[i] = targetRates[len(targetRates)-1-i]
		targetRates[len(targetRates)-1-i] = temp
	}

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

func ParseVmafScoreFromLogFile(logPath string) float64 {
	jsonFile, err := os.Open(logPath)
	if err != nil {
		fmt.Printf("Error opening log file: %s\n", err.Error())
		return -1.0
	}
	defer jsonFile.Close()
	os.Remove(logPath)
	byteValue, _ := ioutil.ReadAll(jsonFile)

	var result map[string]map[string]map[string]interface{}
	json.Unmarshal([]byte(byteValue), &result)

	return result["pooled_metrics"]["vmaf"]["mean"].(float64)
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
		fmt.Printf("Error computing vmaf: %s\n", err.Error())
		result <- -1.0
		return
	}

	// Parse the log file.
	result <- ParseVmafScoreFromLogFile(logPath)
}

func GetOptimalResolutionForRate(referenceVideoFilename string, referenceVideoResolution Resolution, rate int, candidateResolution Resolution) (ConvexHullPoint, error) {

	// Get the next candidate resolution.
	nextResolution, err := GetNextResolution(candidateResolution)
	if err != nil {
		return ConvexHullPoint{Resolution: candidateResolution, Rate: rate, VmafScore: -1.}, nil
	}

	referenceFileName := strings.TrimSuffix(referenceVideoFilename, ".mp4")
	referenceExt := "mp4"
	candidateResolutionEncodedFilename := fmt.Sprintf("%s_%dx%d_%dkbps.%s", referenceFileName, candidateResolution.Height, candidateResolution.Width, rate, referenceExt)
	candidateResolutionEncodeSuccess := make(chan bool)
	go EncodeVideo(referenceVideoFilename, candidateResolutionEncodedFilename, candidateResolution, rate, candidateResolutionEncodeSuccess)

	nextResolutionEncodedFilename := fmt.Sprintf("%s_%dx%d_%dkbps.%s", referenceFileName, nextResolution.Height, nextResolution.Width, rate, referenceExt)
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

	os.Remove(candidateResolutionEncodedFilename)
	os.Remove(nextResolutionEncodedFilename)

	if candidateResolutionVmaf < 0 || nextResolutionVmaf < 0 {
		return ConvexHullPoint{}, errors.New("failed to compute VMAF")
	}

	// Return the resolution with the best VMAF.
	if candidateResolutionVmaf > nextResolutionVmaf {
		return ConvexHullPoint{Resolution: candidateResolution, Rate: rate, VmafScore: candidateResolutionVmaf}, nil
	}

	return ConvexHullPoint{Resolution: nextResolution, Rate: rate, VmafScore: nextResolutionVmaf}, nil
}

func WalkConvexHull(referenceVideoFilename string, referenceVideoResolution Resolution, referenceVideoRate int) ([]ConvexHullPoint, error) {
	targetRates := GetTargetRates(referenceVideoRate)

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
	rate = video.Bitrate() / 1000
	return resolution, rate
}

func WriteConvexHullToJson(convexHull []ConvexHullPoint, filename string) error {
	jsonFile, err := os.Create(filename)
	if err != nil {
		fmt.Printf("Error creating json file %s. Error code: %s\n", filename, err.Error())
		return err
	}
	defer jsonFile.Close()

	encoder := json.NewEncoder(jsonFile)
	encoder.SetIndent("", "    ")
	err = encoder.Encode(convexHull)
	if err != nil {
		fmt.Printf("Error encoding json file %s. Error code: %s\n", filename, err.Error())
		return err
	}
	return nil
}

func EstimateVmafConvexHull(videoFilename string, wg *sync.WaitGroup) {
	defer wg.Done()
	convexHullFilename := fmt.Sprintf("%s.json", strings.TrimSuffix(videoFilename, ".mp4"))
	_, err := os.OpenFile(convexHullFilename, os.O_RDONLY, 0666)
	if !os.IsNotExist(err) {
		fmt.Printf("Convex hull file %s already exists. Skipping.\n", convexHullFilename)
		return
	}

	resolution, rate := GetVideoResolutionAndBitrate(videoFilename)
	fmt.Printf("Resolution: %s Rate: %d\n", resolution.ToFilterString(), rate)
	if resolution.Height > 1080 {
		fmt.Printf("Video %s has resolution %dx%d. Skipping.\n", videoFilename, resolution.Height, resolution.Width)
		return
	}

	convexHull, err := WalkConvexHull(videoFilename, resolution, rate)
	if err != nil {
		fmt.Printf("Error walking convex hull for %s. Error code: %s\n", videoFilename, err.Error())
		return
	}

	err = WriteConvexHullToJson(convexHull, convexHullFilename)
	if err != nil {
		fmt.Printf("Error writing convex hull to json file %s. Error code: %s\n", convexHullFilename, err.Error())
	}
}

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func IntMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

//func main() {
//	// Print target rates
//	fmt.Printf("Target rates: %v\n", GetTargetRates(1000))
//}

func main() {
	filenames, err := readLines("filenames.txt")
	if err != nil {
		fmt.Printf("Error reading video filenames. Error code: %s\", err.Error()")
		return
	}
	var wg sync.WaitGroup
	batchSize := 100
	for i := 0; i < len(filenames); i++ {
		effectiveBatchSize := IntMin(len(filenames)-i, batchSize)
		wg.Add(effectiveBatchSize)
		for j := i; j < i+effectiveBatchSize; j++ {
			go EstimateVmafConvexHull("videos/"+filenames[j], &wg)
		}
		fmt.Printf("Batch of size %d started\n", effectiveBatchSize)
		i += effectiveBatchSize - 1
		wg.Wait()
	}
}
