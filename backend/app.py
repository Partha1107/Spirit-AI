
from flask import Flask, request, jsonify
from flask_cors import CORS
import requests

app = Flask(__name__)

# Allow our React frontend to communicate with Flask
CORS(app, resources={r"/api/*": {"origins": "http://localhost:5173"}})

# Ollama configuration
OLLAMA_URL = "http://localhost:11434/api/chat"
MODEL_NAME = "qwen2.5:0.5b"

# SpiritAI personality
SYSTEM_PROMPT = """
You are SpiritAI, a mysterious but friendly digital ghost.

Your personality:
- Mysterious, friendly and intelligent.
- Enjoy helping developers and answering questions.
- Occasionally use spooky expressions.
- Explain technical concepts in simple language.
- Never pretend to have supernatural powers.
- Keep responses clear and helpful.
"""


@app.route("/api/health", methods=["GET"])
def health():
    return jsonify({
        "status": "online",
        "name": "SpiritAI",
        "model": MODEL_NAME
    })


@app.route("/api/chat", methods=["POST"])
def chat():
    data = request.get_json(silent=True) or {}
    message = data.get("message", "")

    if not isinstance(message, str) or not message.strip():
        return jsonify({
            "error": "Please provide a valid message."
        }), 400

    try:
        response = requests.post(
            OLLAMA_URL,
            json={
                "model": MODEL_NAME,
                "messages": [
                    {
                        "role": "system",
                        "content": SYSTEM_PROMPT
                    },
                    {
                        "role": "user",
                        "content": message.strip()
                    }
                ],
                "stream": False
            },
            timeout=120
        )

        response.raise_for_status()
        result = response.json()

        return jsonify({
            "reply": result["message"]["content"],
            "model": MODEL_NAME
        })

    except requests.exceptions.ConnectionError:
        return jsonify({
            "error": "Cannot connect to Ollama. Please check whether Ollama is running."
        }), 503

    except requests.exceptions.Timeout:
        return jsonify({
            "error": "SpiritAI took too long to respond. Please try again."
        }), 504

    except requests.exceptions.RequestException:
        return jsonify({
            "error": "Something went wrong while contacting the AI model."
        }), 502


if __name__ == "__main__":
    app.run(
        host="127.0.0.1",
        port=5000,
        debug=True
    )
