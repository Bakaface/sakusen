package db

import (
	"path/filepath"
	"testing"
)

func TestCreateTaskWithImages(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}

	// Create task with images
	images := []string{"/path/to/image1.png", "/path/to/image2.jpg"}
	task, err := database.CreateTask(proj.ID, "Task with images", "Description", "task-with-images", "", "", "pending", images)
	if err != nil {
		t.Fatal(err)
	}

	if len(task.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(task.Images))
	}
	if task.Images[0] != "/path/to/image1.png" {
		t.Errorf("expected first image to be '/path/to/image1.png', got '%s'", task.Images[0])
	}
	if task.Images[1] != "/path/to/image2.jpg" {
		t.Errorf("expected second image to be '/path/to/image2.jpg', got '%s'", task.Images[1])
	}

	// Verify we can retrieve the task and images persist
	retrieved, err := database.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(retrieved.Images) != 2 {
		t.Fatalf("expected 2 images on retrieved task, got %d", len(retrieved.Images))
	}
	if retrieved.Images[0] != "/path/to/image1.png" {
		t.Errorf("expected first image to be '/path/to/image1.png', got '%s'", retrieved.Images[0])
	}
	if retrieved.Images[1] != "/path/to/image2.jpg" {
		t.Errorf("expected second image to be '/path/to/image2.jpg', got '%s'", retrieved.Images[1])
	}
}

func TestCreateTaskWithoutImages(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}

	// Create task without images
	task, err := database.CreateTask(proj.ID, "Task without images", "Description", "task-without-images", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(task.Images) != 0 {
		t.Fatalf("expected no images, got %v", task.Images)
	}

	// Verify we can retrieve the task and it has no images
	retrieved, err := database.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(retrieved.Images) != 0 {
		t.Fatalf("expected no images on retrieved task, got %v", retrieved.Images)
	}
}

func TestCreateTaskWithEmptyImages(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}

	// Create task with empty images slice
	task, err := database.CreateTask(proj.ID, "Task with empty images", "Description", "task-with-empty-images", "", "", "pending", []string{})
	if err != nil {
		t.Fatal(err)
	}

	if len(task.Images) != 0 {
		t.Fatalf("expected no images, got %v", task.Images)
	}
}
