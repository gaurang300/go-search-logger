# Go Search Logger

## Overview
Go Search Logger is a simple application that logs user search queries into a PostgreSQL database. It is designed to handle both logged-in users and guests in a multi-server environment, ensuring that only the most complete version of each search term is stored.

## Project Structure
```
go-search-logger
├── cmd
│   └── main.go          # Entry point of the application
├── internal
│   ├── database
│   │   └── db.go       # Database connection logic
│   ├── logger
│   │   └── search_logger.go # Logging functionality
│   ├── models
│   │   └── search.go    # Search entry model
│   ├── server
│   │   └── server.go    # HTTP server setup
│   └── user
│       └── user.go      # User-related logic
├── config
│   └── config.yaml      # Configuration settings
├── go.mod               # Module definition
├── go.sum               # Module checksums
└── README.md            # Project documentation
```

## Setup Instructions

1. **Clone the Repository**
   ```bash
   git clone <repository-url>
   cd go-search-logger
   ```

2. **Set Up Dependencies**

   - **Redis**:  
     Install and start Redis.  
     On macOS:  
     ```bash
     brew install redis
     brew services start redis
     ```
     On Linux:  
     ```bash
     sudo apt-get install redis-server
     sudo systemctl start redis
     ```
     Enable keyspace notifications (required for Pub/Sub features):  
     ```bash
     redis-cli CONFIG SET notify-keyspace-events Ex
     ```

   - **PostgreSQL**:  
     Install and start PostgreSQL.  
     On macOS:  
     ```bash
     brew install postgresql
     brew services start postgresql
     ```
     On Linux:  
     ```bash
     sudo apt-get install postgresql
     sudo systemctl start postgresql
     ```

   - **Go Modules**:  
     Ensure you have Go installed. Download dependencies:  
     ```bash
     go mod tidy
     ```

3. **Configure Database**
   Update the `config/config.go` file with your database connection details.

4. **Run the Application**
   Start the application by running:
   ```bash
   go run cmd/main.go
   ```
   ```bash
   curl -X POST "http://localhost:8080/search" -d 'q=b&user_id=123'
   curl -X POST "http://localhost:8080/search" -d 'q=bu&user_id=123'
   curl -X POST "http://localhost:8080/search" -d 'q=bus&user_id=123'
   curl -X POST "http://localhost:8080/search" -d 'q=busi&user_id=123'
   curl -X POST "http://localhost:8080/search" -d 'q=business&user_id=123'
   curl -X POST "http://localhost:8080/search" -d 'q=business'
   curl -X POST "http://localhost:8080/search" -d 'q=career&user_id=123'
   ```



## Usage
- The application exposes an API endpoint for logging searches. You can send a POST request to the server with the search query and user information.
- The application will log the search term in the database, ensuring that only the most complete version of the search term is stored.
