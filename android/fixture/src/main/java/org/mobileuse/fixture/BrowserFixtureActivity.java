package org.mobileuse.fixture;

import android.app.Activity;
import android.os.Bundle;
import android.view.WindowInsets;
import android.webkit.WebView;
import android.widget.EditText;
import android.widget.LinearLayout;

public final class BrowserFixtureActivity extends Activity {
    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        WebView fixture = new WebView(this);
        fixture.setFocusable(false);
        WebView.setWebContentsDebuggingEnabled(true);
        fixture.getSettings().setJavaScriptEnabled(true);
        LinearLayout layout = new LinearLayout(this);
        layout.setOrientation(LinearLayout.VERTICAL);
        layout.setFocusableInTouchMode(true);
        layout.setOnApplyWindowInsetsListener((view, insets) -> {
            var bars = insets.getInsets(WindowInsets.Type.systemBars());
            view.setPadding(bars.left, bars.top, bars.right, bars.bottom);
            return insets;
        });
        EditText text = new EditText(this);
        text.setSingleLine(true);
        text.setHint("Native test input");
        layout.addView(text, new LinearLayout.LayoutParams(-1, -2));
        layout.addView(fixture, new LinearLayout.LayoutParams(-1, 0, 1));
        setContentView(layout);
        fixture.loadDataWithBaseURL("https://mobile-use.test/", """
            <!doctype html><html lang="en"><meta charset="utf-8">
            <meta name="viewport" content="width=device-width,initial-scale=1">
            <title>Mobile-use browser fixture</title>
            <h1>Mobile-use browser fixture</h1>
            <label for="sample">Sample input</label><input id="sample" type="text">
            <button id="apply">Apply</button><h2 id="result"></h2>
            <script>document.getElementById('apply').onclick = () => {
              document.getElementById('result').textContent = document.getElementById('sample').value;
            };</script></html>
            """, "text/html", "UTF-8", null);
    }
}
