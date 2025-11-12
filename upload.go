package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// 文件上传状态记录
type UploadStatus struct {
	TotalChunks int       `json:"total_chunks"`
	Uploaded    []bool    `json:"uploaded"`
	Filename    string    `json:"filename"`
	UUID        string    `json:"uuid"`
	Path        string    `json:"path"`
	Completed   bool      `json:"completed"`
	Size        int64     `json:"size"`
	UploadedAt  time.Time `json:"uploaded_at"`
}

// 全局上传状态记录
var uploadsMutex sync.Mutex
var uploads = make(map[string]*UploadStatus)

func main() {
	// 创建上传目录
	os.MkdirAll("/home/datawork/uploads", 0755)
	os.MkdirAll("/home/datawork/chunks", 0755)

	// 注册路由
	http.HandleFunc("/api/upload", handleUpload)
	http.HandleFunc("/api/status", handleStatus)
	http.HandleFunc("/api/merge", handleMerge)
	http.HandleFunc("/", serveIndex)

	// 启动服务器
	fmt.Println("服务器启动在 http://10.25.77.4:8999")
	log.Fatal(http.ListenAndServe(":8999", nil))
}

// 处理文件块上传
func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	// 解析表单数据
	err := r.ParseMultipartForm(32 << 20) // 32MB 缓冲区
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 获取文件信息
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 获取上传参数
	uuid := r.FormValue("uuid")
	chunkIndex := r.FormValue("chunkIndex")
	totalChunks := r.FormValue("totalChunks")
	filename := r.FormValue("filename")
	fileSize := r.FormValue("fileSize")

	// 验证参数
	if uuid == "" || chunkIndex == "" || totalChunks == "" || filename == "" || fileSize == "" {
		http.Error(w, "缺少必要参数", http.StatusBadRequest)
		return
	}

	// 转换参数类型
	index, err := strconv.Atoi(chunkIndex)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	total, err := strconv.Atoi(totalChunks)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	size, err := strconv.ParseInt(fileSize, 10, 64)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 创建文件块目录
	chunkDir := filepath.Join("/home/datawork/chunks", uuid)
	os.MkdirAll(chunkDir, 0755)

	// 保存文件块
	chunkPath := filepath.Join(chunkDir, fmt.Sprintf("%d", index))
	out, err := os.Create(chunkPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()

	_, err = io.Copy(out, file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 更新上传状态
	uploadsMutex.Lock()
	defer uploadsMutex.Unlock()

	// 初始化上传状态（如果不存在）
	if _, exists := uploads[uuid]; !exists {
		uploads[uuid] = &UploadStatus{
			TotalChunks: total,
			Uploaded:    make([]bool, total),
			Filename:    filename,
			UUID:        uuid,
			Path:        filepath.Join("/home/datawork/uploads", filename),
			Completed:   false,
			Size:        size,
			UploadedAt:  time.Now(),
		}
	}

	// 标记当前块已上传
	if index < len(uploads[uuid].Uploaded) {
		uploads[uuid].Uploaded[index] = true
	}

	// 检查是否所有块都已上传
	allUploaded := true
	for _, uploaded := range uploads[uuid].Uploaded {
		if !uploaded {
			allUploaded = false
			break
		}
	}

	if allUploaded {
		uploads[uuid].Completed = true
	}

	// 返回当前上传状态
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(uploads[uuid])
}

// 获取上传状态
func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	uuid := r.URL.Query().Get("uuid")
	if uuid == "" {
		http.Error(w, "缺少uuid参数", http.StatusBadRequest)
		return
	}

	uploadsMutex.Lock()
	defer uploadsMutex.Unlock()

	if status, exists := uploads[uuid]; exists {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	} else {
		http.Error(w, "找不到上传记录", http.StatusNotFound)
	}
}

// 合并文件块
func handleMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	uuid := r.FormValue("uuid")
	if uuid == "" {
		http.Error(w, "缺少uuid参数", http.StatusBadRequest)
		return
	}

	uploadsMutex.Lock()
	defer uploadsMutex.Unlock()

	if status, exists := uploads[uuid]; exists {
		if !status.Completed {
			http.Error(w, "文件尚未上传完成", http.StatusBadRequest)
			return
		}

		// 创建目标文件
		out, err := os.Create(status.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer out.Close()

		// 按顺序合并文件块
		chunkDir := filepath.Join("/home/datawork/chunks", uuid)
		for i := 0; i < status.TotalChunks; i++ {
			chunkPath := filepath.Join(chunkDir, fmt.Sprintf("%d", i))
			chunk, err := os.Open(chunkPath)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			_, err = io.Copy(out, chunk)
			chunk.Close()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// 删除已合并的块
			os.Remove(chunkPath)
		}

		// 删除临时目录
		os.RemoveAll(chunkDir)

		// 返回成功信息
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "success",
			"path":   status.Path,
		})
	} else {
		http.Error(w, "找不到上传记录", http.StatusNotFound)
	}
}

// 提供前端页面
func serveIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "./index.html")
}
