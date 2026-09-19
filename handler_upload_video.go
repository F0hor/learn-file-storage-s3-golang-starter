package main

import (
	"fmt"
	"net/http"
	"io"
	"os"
	"os/exec"
	"mime"
	"encoding/base64"
	"crypto/rand"
	"context"
	"bytes"
	"encoding/json"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Video by different user", err)
		return
	}

	fmt.Println("uploading video file for video", videoID, "by user", userID)

	http.MaxBytesReader(w, r.Body, 1 << 30)

	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()
	contentType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))

	fileExtension := contentTypeToFileExtention(contentType)
	if fileExtension != "mp4" {
		respondWithError(w, http.StatusBadRequest, "Wrong content type", fmt.Errorf("Unsupported file type", fileExtension))
		return
	}
	
	tempFile, err := os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to save file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to save file", err)
		return
	}

	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to read file", fmt.Errorf("File seek problem"))
		return
	}

	aspect, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to read file", err)
		return
	}

	var prefix string
	if aspect == "16:9" {
		prefix = "landscape"
	} else if aspect == "9:16" {
		prefix = "portrait"
	} else {
		prefix = "other"
	}

	key := make([]byte, 32)
	rand.Read(key)
	fileName := prefix + "/" + base64.RawURLEncoding.EncodeToString(key) + "." + fileExtension

	putObjectInput := s3.PutObjectInput{
		Bucket: &cfg.s3Bucket,
		Key: &fileName,
		Body: tempFile,
		ContentType: &contentType,
	}
	_, err = cfg.s3Client.PutObject(context.Background(), &putObjectInput)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to upload file", err)
		return
	}

	videoUrl := fmt.Sprintf("https://%v.s3.%v.amazonaws.com/%v", cfg.s3Bucket, cfg.s3Region, fileName)
	video.VideoURL = &videoUrl
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to save video", err)
		return
	}
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var b bytes.Buffer
	cmd.Stdout = &b

	err := cmd.Run()
	if err != nil {
		return "", err
	}

	var probe map[string]interface{}
	err = json.Unmarshal(b.Bytes(), &probe)
	if err != nil {
		return "", err
	}

	/*
	var data map[string]interface{}
	err = json.Unmarshal(probe["streams"].([]interface{})[0].([]byte), &data)
	if err != nil {
		return "", err
	}
	*/

	width := probe["streams"].([]interface{})[0].(map[string]interface{})["width"].(float64)
	height := probe["streams"].([]interface{})[0].(map[string]interface{})["height"].(float64)

	if 16. / 9. - 0.001 <= width / height && width / height <= 16. / 9. + 0.001 {
		return "16:9", nil
	}
	if 9. / 16. - 0.001 <= width / height && width / height <= 9. / 16. + 0.001 {
		return "9:16", nil
	}
	return "other", nil
}
