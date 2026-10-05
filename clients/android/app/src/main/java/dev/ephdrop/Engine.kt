package dev.ephdrop

import android.os.Handler
import android.os.Looper
import dev.ephdrop.mobile.Agent

/** Holds the running Go program so the service and the window can share it. */
object Engine {
    @Volatile
    var agent: Agent? = null
        private set

    @Volatile
    var error: String? = null
        private set

    private val waiting = mutableListOf<(Agent) -> Unit>()
    private val main = Handler(Looper.getMainLooper())

    /** Runs [then] on the main thread as soon as the program is running. */
    fun whenReady(then: (Agent) -> Unit) {
        val a: Agent?
        synchronized(waiting) {
            a = agent
            if (a == null) waiting.add(then)
        }
        if (a != null) main.post { then(a) }
    }

    fun set(a: Agent) {
        val todo: List<(Agent) -> Unit>
        synchronized(waiting) {
            agent = a
            error = null
            todo = waiting.toList()
            waiting.clear()
        }
        main.post { todo.forEach { it(a) } }
    }

    fun fail(message: String) {
        error = message
    }

    fun clear() {
        synchronized(waiting) { agent = null }
    }
}
